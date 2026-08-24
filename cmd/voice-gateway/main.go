// Package main starts the FluentWork voice gateway entrypoint.
package main

import (
	"fmt"

	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
)

func main() {
	fmt.Println(buildinfo.Greeting("voice-gateway"))
}
