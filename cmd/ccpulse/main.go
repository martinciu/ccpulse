package main

import "fmt"

const version = "v0.0.0"

func versionString() string {
	return "ccpulse " + version
}

func main() {
	fmt.Println(versionString() + " — placeholder")
}
