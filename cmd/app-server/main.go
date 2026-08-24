// Package main starts the FluentWork app server entrypoint.
package main

import (
	"fmt"

	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
)

func main() {
	fmt.Println(buildinfo.Greeting("app-server"))
}
