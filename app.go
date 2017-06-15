package main

import (
	"cloudkarafka-mgmt/kafka"
	"cloudkarafka-mgmt/server"
	"cloudkarafka-mgmt/zookeeper"

	"fmt"
	"os"
	"os/signal"
	"time"
)

var (
	port = ":8080"
)

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)

	kafka.Start()
	zookeeper.Start()
	go server.Start(port)

	fmt.Println("CloudKarafka mgmt interface for Apache Kafka started")
	<-signals
	time.AfterFunc(time.Second*2, func() {
		fmt.Println("[ERROR] could not exit in reasonable time")
		os.Exit(1)
	})
	kafka.Stop()
	zookeeper.Stop()
	fmt.Println("stopped")
}