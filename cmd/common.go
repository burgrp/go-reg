package cmd

func json_serializer(value string) []byte {
	return []byte(value)
}

func json_deserializer(value []byte) string {
	return string(value)
}
