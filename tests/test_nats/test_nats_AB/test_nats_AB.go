package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	nuid "github.com/nats-io/nuid"
)

/*
* This code uses modified versions of the following nats examples:
* 	https://github.com/nats-io/nats.go
* 	https://github.com/nats-io/nats.go/blob/main/examples/nats-qsub/main.go
*	https://github.com/nats-io/nats.go/blob/main/examples/nats-pub/main.go
* In this test scenario there are 2 message queues (subjectA, subjectB) and 2 nats connections (connectionA, connectionB).
* connectionA sends messages to subjectB. Those messageIds are added to a string set.
* connectionB receives messages from subjectB as groupB and resends them to subjectA.
* connectionA receives messages from subjectA as groupA. Those messageIds are removed from the string set.
* A successful test will have no errors and no 'lost' messages in the string set.
 */

const (
	appName         = "test-nats-AB"
	natsUrl         = "nats://127.0.0.1:4222"
	connectionNameA = "TestConnectionA"
	connectionNameB = "TestConnectionB"
	subjectA        = "test.nats.A"
	groupA          = "groupA"
	subjectB        = "test.nats.B"
	groupB          = "groupB"
	messageIdKey    = "ics_message_id"
)

type stringSet struct {
	sync.Mutex
	m map[string]bool
}

func (rcvr *stringSet) init() {
	rcvr.Lock()
	defer rcvr.Unlock()
	rcvr.m = make(map[string]bool)
}

func (rcvr *stringSet) add(messageID string) int {
	rcvr.Lock()
	defer rcvr.Unlock()
	_, exists := rcvr.m[messageID]
	if exists {
		log.Fatalf("ERROR: MessageSet.add detected dupe message, messageID: %s", messageID)
	}
	rcvr.m[messageID] = true
	return len(rcvr.m)
}

func (rcvr *stringSet) remove(messageID string) int {
	rcvr.Lock()
	defer rcvr.Unlock()
	delete(rcvr.m, messageID)
	return len(rcvr.m)
}

func (rcvr *stringSet) count() int {
	rcvr.Lock()
	defer rcvr.Unlock()
	return len(rcvr.m)
}

func (rcvr *stringSet) print() {
	rcvr.Lock()
	defer rcvr.Unlock()
	for k := range rcvr.m {
		log.Printf("REMAINDER: MessageID: %s", k)
	}
}

func getUniqueId() string {
	return nuid.New().Next()
}

func waitWithReason(duration time.Duration, iterations int, reason string) {
	for i := 0; i < iterations; i++ {
		log.Printf("Waiting: duration: %s, reason: %s", (time.Duration(iterations-i) * duration).String(), reason)
		time.Sleep(duration)
	}
}

func setupConnectionOptions(opts []nats.Option, name string) []nats.Option {
	totalReconnectWait := 60 * time.Second
	reconnectDelay := time.Second
	flusherTimeout := 60 * time.Second
	drainTimeout := 60 * time.Second
	opts = append(opts, nats.Name(name))
	opts = append(opts, nats.ReconnectWait(reconnectDelay))
	opts = append(opts, nats.MaxReconnects(int(totalReconnectWait/reconnectDelay)))
	opts = append(opts, nats.FlusherTimeout(flusherTimeout))
	opts = append(opts, nats.DrainTimeout(drainTimeout))
	opts = append(opts, nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
		log.Printf("Disconnected: err: %v, will attempt reconnects for %s", err, totalReconnectWait.String())
	}))
	opts = append(opts, nats.ReconnectHandler(func(nc *nats.Conn) {
		log.Printf("Reconnected: url: %s", nc.ConnectedUrl())
	}))
	opts = append(opts, nats.ClosedHandler(func(nc *nats.Conn) {
		log.Printf("Closed: err: %v", nc.LastError())
	}))
	opts = append(opts, nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
		log.Printf("AsyncError: err: %v", nc.LastError())
	}))
	return opts
}

func main() {
	log.Print(appName + " Begin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// connectionA will send messages to subjectB and recv messages from subjectA.
	optsA := []nats.Option{}
	optsA = setupConnectionOptions(optsA, connectionNameA)
	connectionA, err := nats.Connect(natsUrl, optsA...)
	if err != nil {
		log.Panicf("ERROR-A: nats.Connect failed, err: %v", err)
	}
	defer connectionA.Close()
	log.Print("Created connectionA")
	// connectionB will recv messages from subjectB and resend those messages to subjectA.
	optsB := []nats.Option{}
	optsB = setupConnectionOptions(optsB, connectionNameB)
	connectionB, err := nats.Connect(natsUrl, optsB...)
	if err != nil {
		log.Panicf("ERROR-B: nats.Connect failed, err: %v", err)
	}
	defer connectionB.Close()
	log.Print("Created connectionB")
	// messageSet will track connectionA send/recv messages.
	messageSet := stringSet{}
	messageSet.init()
	// Start connectionA receive and print routine.
	log.Print("Running connectionA.QueueSubscribe")
	if _, err = connectionA.QueueSubscribe(subjectA, groupA, func(msg *nats.Msg) {
		messageId := msg.Header.Get(messageIdKey)
		remainderCount := messageSet.remove(messageId)
		log.Printf("RECV-A: MessageId: %s, Body: %s, Remainder: %d", messageId, string(msg.Data), remainderCount)
	}); err != nil {
		log.Panicf("ERROR-A: nats.Conn.QueueSubscribe failed, err: %v", err)
	}
	// Start connectionB receive and print and resend routine.
	log.Print("Running connectionB.QueueSubscribe")
	if _, err = connectionB.QueueSubscribe(subjectB, groupB, func(msg *nats.Msg) {
		messageId := msg.Header.Get(messageIdKey)
		message := string(msg.Data)
		log.Printf("RECV-B: MessageId: %s, Body: %s", messageId, message)
		msg.Subject = msg.Reply
		if err := connectionB.PublishMsg(msg); err != nil {
			log.Panicf("ERROR-B: nats.Conn.PublishMsg failed, err: %v", err)
		}
		log.Printf("SEND-B: MessageId: %s, Body: %s", messageId, message)
	}); err != nil {
		log.Panicf("ERROR-B: nats.Conn.QueueSubscribe failed, err: %v", err)
	}
	// Start connectionA send routine.
	wg := new(sync.WaitGroup)
	wg.Add(1)
	log.Print("Running connectionA.PublishMsg loop")
	go func() {
		msg := nats.NewMsg(subjectB)
		msg.Reply = subjectA
		for i := 1; i <= 1000; i++ {
			messageId := getUniqueId()
			messageSet.add(messageId)
			message := fmt.Sprintf("test_message_%d", i)
			msg.Header.Set(messageIdKey, messageId)
			msg.Data = []byte(message)
			if err := connectionA.PublishMsg(msg); err != nil {
				log.Panicf("ERROR-A: nats.Conn.PublishMsg failed, err: %v", err)
			}
			log.Printf("SEND-A: MessageId: %s, Body: %s", messageId, message)
			time.Sleep(20 * time.Millisecond)
		}
		// Force flush all buffered publish data to the wire.
		// Flush sends a ping and waits for a server pong.
		log.Print("Ended connectionA.PublishMsg loop, flushing")
		timeoutCtx, _ := context.WithTimeout(ctx, 10*time.Second)
		if err := connectionA.FlushWithContext(timeoutCtx); err != nil {
			log.Panicf("ERROR-A: nats.Conn.FlushWithContext failed, err: %v", err)
		}
		waitWithReason(time.Second, 10, "Giving time for all messages to arrive")
		messageSet.print()
		lostCount := messageSet.count()
		if 0 != lostCount {
			log.Panicf("ERROR: Lost Messge Count: %d", lostCount)
		}
		wg.Done() // Don't let app exit.
	}()
	log.Print("Waiting on wg.Done")
	wg.Wait()
	log.Print(appName + " End")
}
