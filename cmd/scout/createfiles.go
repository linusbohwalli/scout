package main

import (
	"fmt"
	"os"
)

func createfiles() {
	for index := 0; index < 1000000; index++ {
		fname := fmt.Sprintf("%v", index)
		file, err := os.OpenFile(fname, os.O_CREATE|os.O_WRONLY, 0660)
		if err != nil {
			panic(err)
		}
		file.Close()

	}
}
