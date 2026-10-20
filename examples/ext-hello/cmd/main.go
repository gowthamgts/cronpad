package main

import (
	hello "github.com/gowthamgts/cronpad/examples/ext-hello"
	"github.com/gowthamgts/cronpad/pkg/cronpad"
)

func main() { cronpad.Main(cronpad.WithExtension(hello.New()), cronpad.WithEdition("hello")) }
