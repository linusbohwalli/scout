package main

import (
	"fmt"
	"log"
	"os"

	"github.com/linusbohwalli/scout"
)

func main() {

	if len(os.Args) == 1 {
		fmt.Println("usage: fileEventEmitter <command> ")
		fmt.Println("fileEventEmitter -h or -help for more information")
		return
	}

	sct, err := scout.NewScout()
	if err != nil {
		log.Fatalf("unable to initialze new file event scout: %v", err)
	}

	switch os.Args[1] {
	case "start":
		fmt.Println("Service successfully started enjoy!")

		sct.Start()

	case "stop":
		fmt.Println("Shutdown signal received...")
		//fee.Stop()

	default:
		fmt.Println("Command does not exist")
	}
}
