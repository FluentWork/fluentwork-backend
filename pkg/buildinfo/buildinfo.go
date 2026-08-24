package buildinfo

const Repository = "fluentwork-backend"

func Greeting(name string) string {
	return "Hello, " + name + "."
}

func VerificationPointerValue(value *string) string {
	return *value
}
