package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	nuid "github.com/nats-io/nuid"
)

/*
* This code uses modified versions of the following nats examples:
* 	https://github.com/nats-io/nats.go
*	https://github.com/nats-io/nats.go/blob/main/example_test.go
*	https://github.com/nats-io/nats.go/blob/main/js_test.go
* 	https://github.com/nats-io/nats.go/blob/main/examples/nats-qsub/main.go
*	https://github.com/nats-io/nats.go/blob/main/examples/nats-pub/main.go
* In this test scenario there are 2 message queues (subjectA, subjectB),
* and 3 nats connections (connectionA, connectionB, connectionC),
* and 3 nats jetstream contexts (jetstreamContextA, jetstreamContextB, jetstreamContextC).
* jetstreamContextA sends messages to subjectB. Those messageIds are added to a string set.
* jetstreamContextB or jetstreamContextC receives messages from subjectB as groupB and resends them to subjectA.
* jetstreamContextA receives messages from subjectA as groupA. Those messageIds are removed from the string set.
* A successful test will have no errors and no 'lost' messages in the string set.
* We are testing subjectB message sharing among connectionB and connectionC.
* In this test we expect ackCountA = ackCountB + ackCountC, because both B and C share groupB, so the messages are 'load balanced'.
 */

const (
	appName         = "test-nats-stream-pull-ABC"
	natsUrlLocal    = "nats://127.0.0.1:4222"
	natsUrlMinikube = "nats://127.0.0.1:4222"
	natsUrlGKE      = "nats://nats.default.svc.cluster.local:4222"
	streamName      = "TestStreamAB"
	connectionNameA = "TestConnectionA"
	connectionNameB = "TestConnectionB"
	connectionNameC = "TestConnectionC"
	consumerNameA   = "TestConsumerA"
	consumerNameB   = "TestConsumerB"
	consumerNameC   = "TestConsumerC"
	subjectWild     = "test.nats.*"
	subjectA        = "test.nats.A"
	subjectB        = "test.nats.B"
	subjectC        = "test.nats.B" // Same as B.
	groupA          = "groupA"
	groupB          = "groupB"
	groupC          = "groupB" // Same as B.
	messageIdKey    = "ics_message_id"
)

var (
	ackCountA int
	ackCountB int
	ackCountC int
)

func getDefaultConfigEnv() string {
	configEnv, exists := os.LookupEnv("ICS_DATA_CFGS_ENV")
	if exists {
		return configEnv
	}
	return "local"
}

func getNatsUrl(configEnv string) string {
	switch configEnv {
	case "local":
		return natsUrlLocal
	case "minikube":
		return natsUrlMinikube
	}
	return natsUrlGKE
}

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

func initStream(js nats.JetStreamContext) {
	uninitStream(js)
	log.Println("Init stream")
	// Create stream.
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     streamName,
		Subjects: []string{subjectWild},
	}); err != nil {
		if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			log.Panicf("ERROR: nats.JetStreamContext.AddStream failed, err: %v", err)
		}
	}
	// Create consumer(s).
	// NATS.io says AddConsumer does not really support 'push' consumers: they should be ephemeral and use the Subscribe API.
	/*
		if _, err := js.AddConsumer(streamName, &nats.ConsumerConfig{
			Durable: consumerNameA,
			//DeliverGroup:   groupA,
			DeliverPolicy:  nats.DeliverNewPolicy,
			DeliverSubject: subjectA,
			//FilterSubject:  subjectA,
			AckPolicy: nats.AckNonePolicy,
			Heartbeat: time.Minute,
		}); err != nil {
			log.Panicf("ERROR: nats.JetStreamContext.AddConsumer failed, err: %v", err)
		}
		if _, err := js.AddConsumer(streamName, &nats.ConsumerConfig{
			Durable: consumerNameB,
			//DeliverGroup:   groupB,
			DeliverPolicy:  nats.DeliverNewPolicy,
			DeliverSubject: subjectB,
			//FilterSubject:  subjectB,
			AckPolicy: nats.AckNonePolicy,
			Heartbeat: time.Minute,
		}); err != nil {
			log.Panicf("ERROR: nats.JetStreamContext.AddConsumer failed, err: %v", err)
		}
	*/
}

func uninitStream(js nats.JetStreamContext) {
	log.Println("Uninit stream")
	// Delete consumer(s).
	if err := js.DeleteConsumer(streamName, consumerNameB); err != nil {
		if !errors.Is(err, nats.ErrConsumerNotFound) {
			log.Panicf("ERROR: nats.JetStreamContext.DeleteConsumer failed, err: %v", err)
		}
	}
	if err := js.DeleteConsumer(streamName, consumerNameA); err != nil {
		if !errors.Is(err, nats.ErrConsumerNotFound) {
			log.Panicf("ERROR: nats.JetStreamContext.DeleteConsumer failed, err: %v", err)
		}
	}
	// Delete stream.
	if err := js.DeleteStream(streamName); err != nil {
		if !errors.Is(err, nats.ErrStreamNotFound) {
			log.Panicf("ERROR: nats.JetStreamContext.DeleteStream failed, err: %v", err)
		}
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
	configEnv := flag.String("configenv", getDefaultConfigEnv(), "ics-data-config environment")
	flag.Parse()
	log.Printf("configEnv: %s", *configEnv)
	natsUrl := getNatsUrl(*configEnv)
	log.Printf("natsUrl: %s", natsUrl)
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
	jetstreamContextA, err := connectionA.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		log.Panicf("ERROR-A: nats.Conn.JetStream failed, err: %v", err)
	}
	log.Print("Created connectionA and jetstreamContextA")
	// connectionB will recv messages from subjectB and resend those messages to subjectA.
	optsB := []nats.Option{}
	optsB = setupConnectionOptions(optsB, connectionNameB)
	connectionB, err := nats.Connect(natsUrl, optsB...)
	if err != nil {
		log.Panicf("ERROR-B: nats.Connect failed, err: %v", err)
	}
	defer connectionB.Close()
	jetstreamContextB, err := connectionB.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		log.Panicf("ERROR-B: nats.Conn.JetStream failed, err: %v", err)
	}
	log.Print("Created connectionB and jetstreamContextB")
	// connectionC will recv messages from subjectB and resend those messages to subjectA.
	optsC := []nats.Option{}
	optsC = setupConnectionOptions(optsC, connectionNameC)
	connectionC, err := nats.Connect(natsUrl, optsC...)
	if err != nil {
		log.Panicf("ERROR-C: nats.Connect failed, err: %v", err)
	}
	defer connectionC.Close()
	jetstreamContextC, err := connectionC.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		log.Panicf("ERROR-C: nats.Conn.JetStream failed, err: %v", err)
	}
	log.Print("Created connectionC and jetstreamContextC")
	// Set up stream.
	initStream(jetstreamContextA)
	// messageSet will track connectionA send/recv messages.
	messageSet := stringSet{}
	messageSet.init()
	wg := new(sync.WaitGroup)
	// Start jetstreamContextA receive and print routine.
	wg.Add(1)
	log.Print("Running jetstreamContextA.PullSubscribe")
	go func() {
		defer wg.Done()
		// Create Pull based consumer with maximum 128 inflight.
		sub, err := jetstreamContextA.PullSubscribe(subjectA, groupA, nats.PullMaxWaiting(128))
		if err != nil {
			log.Panicf("ERROR-A: nats.JetStreamContext.PullSubscribe failed, err: %v", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// We need to pass a new timeout ctx for every Fetch call or else Fetch will do it for us using default timeout.
			timeoutCtx, _ := context.WithTimeout(ctx, 10*time.Second)
			msgs, err := sub.Fetch(10, nats.Context(timeoutCtx))
			if err != nil {
				switch err {
				case context.Canceled:
					fallthrough
				case context.DeadlineExceeded:
					fallthrough
				case nats.ErrTimeout:
					log.Printf("ERROR-A: nats.Subscription.Fetch failed, err: %v", err)
				default:
					log.Panicf("ERROR-A: nats.Subscription.Fetch failed, err: %v", err)
				}
			}
			for _, msg := range msgs {
				ackCountA++
				messageId := msg.Header.Get(messageIdKey)
				remainderCount := messageSet.remove(messageId)
				log.Printf("RECV-A: MessageId: %s, Body: %s, Remainder: %d", messageId, string(msg.Data), remainderCount)
				if err := msg.Ack(); err != nil {
					log.Panicf("ERROR-A: nats.Msg.Ack failed, err: %v", err)
				}
			}
		}
	}()
	// Start jetstreamContextB receive and print and resend routine.
	wg.Add(1)
	log.Print("Running jetstreamContextB.PullSubscribe")
	go func() {
		defer wg.Done()
		// Create Pull based consumer with maximum 128 inflight.
		sub, err := jetstreamContextB.PullSubscribe(subjectB, groupB, nats.PullMaxWaiting(128))
		if err != nil {
			log.Panicf("ERROR-B: nats.JetStreamContext.PullSubscribe failed, err: %v", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// We need to pass a new timeout ctx for every Fetch call or else Fetch will do it for us using default timeout.
			timeoutCtx, _ := context.WithTimeout(ctx, 10*time.Second)
			msgs, err := sub.Fetch(10, nats.Context(timeoutCtx))
			if err != nil {
				switch err {
				case context.Canceled:
					fallthrough
				case context.DeadlineExceeded:
					fallthrough
				case nats.ErrTimeout:
					log.Printf("ERROR-B: nats.Subscription.Fetch failed, err: %v", err)
				default:
					log.Panicf("ERROR-B: nats.Subscription.Fetch failed, err: %v", err)
				}
			}
			for _, msgIn := range msgs {
				ackCountB++
				messageId := msgIn.Header.Get(messageIdKey)
				message := string(msgIn.Data)
				log.Printf("RECV-B: MessageId: %s, Body: %s", messageId, message)
				if err := msgIn.Ack(); err != nil {
					log.Panicf("ERROR-B: nats.Msg.Ack failed, err: %v", err)
				}
				// PublishMsgAsync will error if we try to reuse a msg object.
				msgOut := nats.NewMsg(subjectA)
				msgOut.Header.Set(messageIdKey, messageId)
				msgOut.Data = msgIn.Data
				if _, err := jetstreamContextB.PublishMsgAsync(msgOut, nats.MsgId(getUniqueId())); err != nil {
					log.Panicf("ERROR-B: nats.JetStreamContext.PublishMsgAsync failed, err: %v", err)
				}
				log.Printf("SEND-B: MessageId: %s, Body: %s", messageId, message)
			}
		}
	}()
	// Start jetstreamContextC receive and print and resend routine.
	wg.Add(1)
	log.Print("Running jetstreamContextC.PullSubscribe")
	go func() {
		defer wg.Done()
		// Create Pull based consumer with maximum 128 inflight.
		sub, err := jetstreamContextC.PullSubscribe(subjectC, groupC, nats.PullMaxWaiting(128))
		if err != nil {
			log.Panicf("ERROR-C: nats.JetStreamContext.PullSubscribe failed, err: %v", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// We need to pass a new timeout ctx for every Fetch call or else Fetch will do it for us using default timeout.
			timeoutCtx, _ := context.WithTimeout(ctx, 10*time.Second)
			msgs, err := sub.Fetch(10, nats.Context(timeoutCtx))
			if err != nil {
				switch err {
				case context.Canceled:
					fallthrough
				case context.DeadlineExceeded:
					fallthrough
				case nats.ErrTimeout:
					log.Printf("ERROR-C: nats.Subscription.Fetch failed, err: %v", err)
				default:
					log.Panicf("ERROR-C: nats.Subscription.Fetch failed, err: %v", err)
				}
			}
			for _, msgIn := range msgs {
				ackCountC++
				messageId := msgIn.Header.Get(messageIdKey)
				message := string(msgIn.Data)
				log.Printf("RECV-C: MessageId: %s, Body: %s", messageId, message)
				if err := msgIn.Ack(); err != nil {
					log.Panicf("ERROR-C: nats.Msg.Ack failed, err: %v", err)
				}
				// PublishMsgAsync will error if we try to reuse a msg object.
				msgOut := nats.NewMsg(subjectA)
				msgOut.Header.Set(messageIdKey, messageId)
				msgOut.Data = msgIn.Data
				if _, err := jetstreamContextC.PublishMsgAsync(msgOut, nats.MsgId(getUniqueId())); err != nil {
					log.Panicf("ERROR-C: nats.JetStreamContext.PublishMsgAsync failed, err: %v", err)
				}
				log.Printf("SEND-C: MessageId: %s, Body: %s", messageId, message)
			}
		}
	}()
	// Start connectionA send routine.
	wg.Add(1)
	log.Print("Running jetstreamContextA.PublishMsgAsync loop")
	go func() {
		defer cancel()
		for i := 1; i <= 1000; i++ {
			messageId := getUniqueId()
			messageSet.add(messageId)
			message := fmt.Sprintf("test_message_%d", i)
			// PublishMsgAsync will error if we try to reuse a msg object.
			msg := nats.NewMsg(subjectB)
			// PublishMsgAsync will error if we try to set msg.Reply.
			// msg.Reply = subjectA
			// NATS doesn't allow us to reuse message ids for different subjects: they must be globally unique.
			// So, we need to pass our own message ids via the message header.
			msg.Header.Set(messageIdKey, messageId)
			msg.Data = []byte(message)
			if _, err := jetstreamContextA.PublishMsgAsync(msg, nats.MsgId(getUniqueId())); err != nil {
				log.Panicf("ERROR-A: nats.JetStreamContext.PublishMsgAsync failed, err: %v", err)
			}
			log.Printf("SEND-A: MessageId: %s, Body: %s", messageId, message)
			time.Sleep(20 * time.Millisecond)
		}
		log.Print("Ended jetstreamContextA.PublishMsgAsync loop, waiting for completion event")
		select {
		case <-jetstreamContextA.PublishAsyncComplete():
		case <-time.After(5 * time.Second):
			log.Println("ERROR-A: nats.JetStreamContext.PublishAsyncComplete did not resolve in time")
		}
		waitWithReason(time.Second, 10, "Giving time for all messages to arrive")
		log.Printf("ackCountA: %d", ackCountA)
		log.Printf("ackCountB: %d", ackCountB)
		log.Printf("ackCountC: %d", ackCountC)
		messageSet.print()
		lostCount := messageSet.count()
		if 0 != lostCount {
			log.Panicf("ERROR: Lost Messge Count: %d", lostCount)
		}
		if ackCountA != (ackCountB + ackCountC) {
			log.Panicf("ERROR: ackCountA(%d) != (ackCountB(%d) + ackCountC(%d))", ackCountA, ackCountB, ackCountC)
		}
		wg.Done() // Don't let app exit.
	}()
	log.Print("Waiting on wg.Done")
	wg.Wait()
	// Remove stream.
	uninitStream(jetstreamContextA)
	log.Print(appName + " End")
}
