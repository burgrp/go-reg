package cmd

func json_serializer(value string) []byte {
	return []byte(value)
}

func json_deserializer(value []byte) string {
	if len(value) == 0 {
		return "(none)"
	}
	return string(value)
}
