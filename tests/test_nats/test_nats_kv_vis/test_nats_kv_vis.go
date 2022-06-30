package main

import (
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

func main() {
	nc, _ := nats.Connect(nats.DefaultURL)
	js, _ := nc.JetStream()

	_, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket: "test",
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Print("Testing kv visibility")

	time.Sleep(time.Minute)
}
