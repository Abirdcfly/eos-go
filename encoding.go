package eos

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"time"

	"errors"
	"reflect"

	"encoding/hex"

	"github.com/eoscanada/eos-go/ecc"
)

var TypeSize = struct {
	Byte           int
	UInt16         int
	Int16          int
	UInt32         int
	UInt64         int
	SHA256Bytes    int
	PublicKey      int
	Signature      int
	Tstamp         int
	BlockTimestamp int
}{
	Byte:           1,
	UInt16:         2,
	Int16:          2,
	UInt32:         4,
	UInt64:         8,
	SHA256Bytes:    32,
	PublicKey:      34,
	Signature:      66,
	Tstamp:         8,
	BlockTimestamp: 4,
}

// Decoder implements the EOS unpacking, similar to FC_BUFFER
type Decoder struct {
	data               []byte
	pos                int
	decodeP2PMessage   bool
	decodeTransactions bool
	decodeActions      bool

	//actionMap    map[AccountName]map[ActionName]interface{}
	//actionABIMap map[AccountName]map[ActionName]ABIDef

	//lastSeenAction ActionName
}

var print = func(s string) {
	fmt.Print(s)
}
var println = func(s string) {
	print(fmt.Sprintf("%s\n", s))
}

func NewDecoder(data []byte) *Decoder {
	return &Decoder{
		data:               data,
		decodeP2PMessage:   true,
		decodeTransactions: true,
		decodeActions:      true,
	}
}

func (d *Decoder) DecodeP2PMessage(decode bool) {
	d.decodeP2PMessage = decode
}
func (d *Decoder) Decode(v interface{}) (err error) {

	rv := reflect.Indirect(reflect.ValueOf(v))
	if !rv.CanAddr() {
		return errors.New("decode: can only Decode to pointer type")
	}
	t := rv.Type()

	switch v.(type) {
	case *string:
		s, e := d.readString()
		if e != nil {
			err = e
			return
		}
		rv.SetString(s)
		return
	case *Name, *AccountName, *PermissionName, *ActionName, *TableName, *ScopeName:
		name := NameToString(d.readUint64())
		println(fmt.Sprintf("readName [%s]", name))
		rv.SetString(name)
		return
	case *byte, *P2PMessageType, *TransactionStatus:
		rv.SetUint(uint64(d.readByte()))
		return
	case *int16:
		rv.SetInt(int64(d.readInt16()))
		return
	case *uint16:
		rv.SetUint(uint64(d.readUint16()))
		return
	case *uint32:
		rv.SetUint(uint64(d.readUint32()))
		return
	case *uint64:
		rv.SetUint(uint64(d.readUint64()))
		return
	case *Varuint32:
		var r uint64
		r, err = d.readUvarint()
		rv.SetUint(r)
		return
	case *[]byte:
		var data []byte
		data, err = d.readByteArray()
		rv.SetBytes(data)
		return
	case *SHA256Bytes:
		r := d.readSHA256Bytes()
		rv.SetBytes(r)
		return
	case *ecc.PublicKey:
		r := d.readPublicKey()
		rv.SetBytes(r)
		return
	case *ecc.Signature:
		r := d.readSignature()
		rv.SetBytes(r)
		return
	case *Tstamp:
		r := d.readTstamp()
		rv.Set(reflect.ValueOf(r))
		return
	case *BlockTimestamp:
		r := d.readBlockTimestamp()
		rv.Set(reflect.ValueOf(r))
		return
	case *OptionalProducerSchedule:

		isPresent := d.readByte()

		if isPresent == 0 {
			println("Skipping optional OptionalProducerSchedule")
			return
		}

	case *P2PMessageEnvelope:

		//d.decodeStruct(v, t, rv)

		envelope := d.readP2PMessageEnvelope()

		if d.decodeP2PMessage {
			attr, ok := envelope.Type.Attributes()
			if !ok {
				return fmt.Errorf("decode: unknown p2p message type [%d]", envelope.Type)
			}
			msg := reflect.New(attr.ReflectType)
			subDecoder := NewDecoder(envelope.Payload)
			subDecoder.Decode(msg.Interface())

			decoded := msg.Interface().(P2PMessage)
			envelope.P2PMessage = &decoded
		}

		rv.Set(reflect.ValueOf(*envelope))

		return
	}

	switch t.Kind() {
	case reflect.Array:
		print("Array")
		var len uint64
		len, err = d.readUvarint()
		if err != nil {
			return
		}
		for i := 0; i < int(len); i++ {
			if err = d.Decode(rv.Index(i).Addr().Interface()); err != nil {
				return
			}
		}
		return

	case reflect.Slice:
		print("Reading Slice length ")
		var l uint64
		if l, err = d.readUvarint(); err != nil {
			return
		}
		println(fmt.Sprintf("Slice [%T] of length: %d", v, l))
		rv.Set(reflect.MakeSlice(t, int(l), int(l)))
		for i := 0; i < int(l); i++ {
			if err = d.Decode(rv.Index(i).Addr().Interface()); err != nil {
				return
			}
		}

	case reflect.Struct:

		err = d.decodeStruct(v, t, rv)
		if err != nil {
			return
		}

	case reflect.Map:
		//fmt.Println("Map")
		var l uint64
		if l, err = d.readUvarint(); err != nil {
			return
		}
		kt := t.Key()
		vt := t.Elem()
		rv.Set(reflect.MakeMap(t))
		for i := 0; i < int(l); i++ {
			kv := reflect.Indirect(reflect.New(kt))
			if err = d.Decode(kv.Addr().Interface()); err != nil {
				return
			}
			vv := reflect.Indirect(reflect.New(vt))
			if err = d.Decode(vv.Addr().Interface()); err != nil {
				return
			}
			rv.SetMapIndex(kv, vv)
		}
	default:

		return errors.New("binary: unsupported type " + t.String())

	}
	return
}

func (d *Decoder) decodeStruct(v interface{}, t reflect.Type, rv reflect.Value) (err error) {
	l := rv.NumField()
	for i := 0; i < l; i++ {

		if tag := t.Field(i).Tag.Get("eos"); tag == "-" {
			continue
		}

		if v := rv.Field(i); v.CanSet() && t.Field(i).Name != "_" {
			iface := v.Addr().Interface()
			println(fmt.Sprintf("Struct Field name: %s", t.Field(i).Name))
			if err = d.Decode(iface); err != nil {
				return
			}
		}
	}
	return
}

var VarIntBufferSizeError = fmt.Errorf("varint: invalide buffer size")

func (d *Decoder) readUvarint() (uint64, error) {

	l, read := binary.Uvarint(d.data[d.pos:])
	if read <= 0 {
		println(fmt.Sprintf("readUvarint [%d]", l))
		return l, VarIntBufferSizeError
	}

	d.pos += read
	println(fmt.Sprintf("readUvarint [%d]", l))
	return l, nil
}

func (d *Decoder) readByteArray() (out []byte, err error) {

	l, err := d.readUvarint()
	if err != nil {
		return nil, err
	}

	if len(d.data) < d.pos+int(l) {
		return nil, fmt.Errorf("byte array: varlen=%d, missing %d bytes", l, d.pos+int(l)-len(d.data))
	}

	out = d.data[d.pos : d.pos+int(l)]
	d.pos += int(l)

	println(fmt.Sprintf("readByteArray [%s]", hex.EncodeToString(out)))
	return
}

func (d *Decoder) readByte() (out byte) {
	out = d.data[d.pos]
	d.pos++
	println(fmt.Sprintf("readByte [%d]", out))
	return
}

func (d *Decoder) readUint16() (out uint16) {
	out = binary.LittleEndian.Uint16(d.data[d.pos:])
	d.pos += TypeSize.UInt16
	return
}

func (d *Decoder) readInt16() (out int16) {
	out = int16(d.readUint16())
	return
}

func (d *Decoder) readUint32() (out uint32) {
	out = binary.LittleEndian.Uint32(d.data[d.pos:])
	d.pos += TypeSize.UInt32
	println(fmt.Sprintf("readUint32 [%d]", out))
	return
}

func (d *Decoder) readUint64() (out uint64) {
	out = binary.LittleEndian.Uint64(d.data[d.pos:])
	d.pos += TypeSize.UInt64
	println(fmt.Sprintf("readUint64 [%d]", out))
	return
}

func (d *Decoder) readString() (out string, err error) {
	data, err := d.readByteArray()
	out = string(data)
	println(fmt.Sprintf("readString [%s]", out))
	return
}

func (d *Decoder) readSHA256Bytes() (out SHA256Bytes) {
	out = SHA256Bytes(d.data[d.pos : d.pos+TypeSize.SHA256Bytes])
	d.pos += TypeSize.SHA256Bytes
	println(fmt.Sprintf("readSHA256Bytes [%s]", hex.EncodeToString(out)))
	return
}

func (d *Decoder) readPublicKey() (out ecc.PublicKey) {
	out = ecc.PublicKey(d.data[d.pos : d.pos+TypeSize.PublicKey])
	d.pos += TypeSize.PublicKey
	println(fmt.Sprintf("readPublicKey [%s]", hex.EncodeToString(out)))
	return
}

func (d *Decoder) readSignature() (out ecc.Signature) {
	out = ecc.Signature(d.data[d.pos : d.pos+TypeSize.Signature])
	d.pos += TypeSize.Signature
	println(fmt.Sprintf("readSignature [%s]", hex.EncodeToString(out)))
	return
}

func (d *Decoder) readTstamp() (out Tstamp) {
	unixNano := d.readUint64()
	out.Time = time.Unix(0, int64(unixNano))
	println(fmt.Sprintf("readTstamp [%s]", out))
	return
}

func (d *Decoder) readBlockTimestamp() (out BlockTimestamp) {
	unixSec := int64(d.readUint32())
	out.Time = time.Unix(unixSec+946684800, 0)
	return
}

func (d *Decoder) readP2PMessageEnvelope() (out *P2PMessageEnvelope) {

	out = &P2PMessageEnvelope{}
	out.Length = d.readUint32()
	out.Type = P2PMessageType(d.readByte())

	payload := d.data[d.pos : d.pos+int(out.Length-1)]
	d.pos += int(out.Length)

	out.Payload = payload
	return
}

func (d *Decoder) remaining() int {
	return len(d.data) - d.pos
}

// --------------------------------------------------------------
// Encoder implements the EOS packing, similar to FC_BUFFER
// --------------------------------------------------------------
type Encoder struct {
	output io.Writer
	Order  binary.ByteOrder
	data   []byte
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		output: w,
		Order:  DefaultEndian,
		data:   make([]byte, 0),
	}
}

func (d *Encoder) Encode(v interface{}) (err error) {
	switch cv := v.(type) {
	case string, Name, AccountName, PermissionName, ActionName, TableName, ScopeName:
		d.writeString(cv.(string))
		return
	case byte:
		d.writeByte(cv)
		return
	//case TransactionStatus:
	//	d.writeByte(byte(cv))
	//	return
	case int16:
		d.writeInt16(cv)
		return
	case uint16:
		d.writeUint16(cv)
		return
	case uint32:
		d.writeUint32(cv)
		return
	case uint64:
		d.writeUint64(cv)
		return
	case Varuint32:
		d.writeUVarInt(int(cv))
		return
	case SHA256Bytes:
		d.writeSHA256Bytes(cv)
		return
	case ecc.PublicKey:
		d.writePublicKey(cv)
		return
	case ecc.Signature:
		d.writeSignature(cv)
		return
	case Tstamp:
		d.writeTstamp(cv)
		return
	case BlockTimestamp:
		d.writeBlockTimestamp(cv)
		return
	case *P2PMessageEnvelope:
		d.writeBlockP2PMessageEnvelope(*cv)
		return
	default:

		rv := reflect.Indirect(reflect.ValueOf(v))
		t := rv.Type()

		switch t.Kind() {

		case reflect.Array:
			l := t.Len()
			d.writeUVarInt(l)
			println(fmt.Sprintf("Encode: array [%T] of length: %d", v, l))
			for i := 0; i < l; i++ {
				if err = d.Encode(rv.Index(i).Interface()); err != nil {
					return
				}
			}

		case reflect.Slice:
			l := rv.Len()
			d.writeUVarInt(l)
			println(fmt.Sprintf("Encode: slice [%T] of length: %d", v, l))
			for i := 0; i < l; i++ {
				if err = d.Encode(rv.Index(i).Interface()); err != nil {
					return
				}
			}

		case reflect.Struct:
			l := rv.NumField()
			println(fmt.Sprintf("Encode: struct [%T] of length: %d", v, l))
			n := 0
			for i := 0; i < l; i++ {
				field := t.Field(i)
				println(fmt.Sprintf("Encode: field -> %s", field.Name))

				if tag := field.Tag.Get("eos"); tag == "-" {
					continue
				}
				if v := rv.Field(i); t.Field(i).Name != "_" {
					if v.CanInterface() {
						iface := v.Interface()
						if iface != nil {
							if err = d.Encode(iface); err != nil {
								return
							}
						}
					}
					n++
				}
			}

		case reflect.Map:
			l := rv.Len()
			d.writeUVarInt(l)
			println(fmt.Sprintf("Map [%T] of length: %d", v, l))
			for _, key := range rv.MapKeys() {
				value := rv.MapIndex(key)
				if err = d.Encode(key.Interface()); err != nil {
					return err
				}
				if err = d.Encode(value.Interface()); err != nil {
					return err
				}
			}
		default:
			return errors.New("binary: unsupported type " + t.String())
		}
	}
	return
}

func (e *Encoder) append(bytes []byte) {

	println(fmt.Sprintf("Appending : [%s][%s]", bytes, hex.EncodeToString(bytes)))
	e.data = append(e.data, bytes...)
	return
}

func (e *Encoder) writeByteArray(b []byte) {

	e.writeUVarInt(len(b))
	e.append(b)
}

func (e *Encoder) writeUVarInt(v int) {
	buf := make([]byte, 8)
	l := binary.PutUvarint(buf, uint64(v))
	e.append(buf[:l])
}

func (e *Encoder) writeByte(b byte) {
	e.append([]byte{b})
}

func (e *Encoder) writeUint16(i uint16) {
	buf := make([]byte, TypeSize.UInt16)
	binary.LittleEndian.PutUint16(buf, i)
	e.append(buf)
}

func (e *Encoder) writeInt16(i int16) {
	e.writeUint16(uint16(i))
}

func (e *Encoder) writeUint32(i uint32) {
	buf := make([]byte, TypeSize.UInt32)
	binary.LittleEndian.PutUint32(buf, i)
	e.append(buf)

}

func (e *Encoder) writeUint64(i uint64) {
	buf := make([]byte, TypeSize.UInt64)
	binary.LittleEndian.PutUint64(buf, i)
	e.append(buf)

}

func (e *Encoder) writeString(s string) {
	e.writeByteArray([]byte(s))
}

func (e *Encoder) writeSHA256Bytes(s SHA256Bytes) {
	if len(s) == 0 {
		e.append(bytes.Repeat([]byte{0}, TypeSize.SHA256Bytes))
	}
	e.append(s)
}

func (e *Encoder) writePublicKey(pk ecc.PublicKey) {
	if len(pk) == 0 {
		e.append(bytes.Repeat([]byte{0}, TypeSize.PublicKey))
	}
	e.append(pk)
}

func (e *Encoder) writeSignature(s ecc.Signature) {
	if len(s) == 0 {
		e.append(bytes.Repeat([]byte{0}, TypeSize.Signature))
	}
	e.append(s)
}

func (e *Encoder) writeTstamp(t Tstamp) {
	n := uint64(t.UnixNano())
	e.writeUint64(n)
}

func (e *Encoder) writeBlockTimestamp(bt BlockTimestamp) {
	n := uint32(bt.Unix() - 946684800)
	e.writeUint32(n)
}

func (e *Encoder) writeBlockP2PMessageEnvelope(envelope P2PMessageEnvelope) {

	println("writeBlockP2PMessageEnvelope")

	e.writeUint32(envelope.Length)
	e.writeByte(byte(envelope.Type))
	e.append(envelope.Payload)

}
