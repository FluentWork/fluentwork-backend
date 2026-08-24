package main

import (
	"fmt"

	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
)

func main() {
	fmt.Println(buildinfo.Greeting("voice-gateway"))
}
