package reg

import "strconv"

type Serialize[T any] func(value T) []byte
type Deserialize[T any] func(value []byte) T

func StringSerialize(value string) []byte {
	return []byte(value)
}

func StringDeserialize(value []byte) string {
	return string(value)
}

func NumberSerialize(value float64) []byte {
	return []byte(strconv.FormatFloat(value, 'f', -1, 64))
}

func NumberDeserialize(value []byte) float64 {
	f, _ := strconv.ParseFloat(string(value), 64)
	return f
}

func BoolSerialize(value bool) []byte {
	return []byte(strconv.FormatBool(value))
}

func BoolDeserialize(value []byte) bool {
	b, _ := strconv.ParseBool(string(value))
	return b
}
