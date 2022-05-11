package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	kubemq_queues_stream "github.com/kubemq-io/kubemq-go/queues_stream"
	nuid "github.com/nats-io/nuid"
)

/*
* This code uses a modified version of (https://github.com/kubemq-io/go-sdk-cookbook/blob/main/queues/stream/main.go).
* In this test scenario there are 2 message queues (channelA, channelB) and 2 queue stream clients (queuesClientA, queuesClientB).
* queuesClientA sends messages to channelB. Those messageIDs are added to a string set.
* queuesClientB receives messages from channelB and echos/sends them to channelA (the hard way, not via resend).
* queuesClientA receives messages from channelA. Those messageIDs are removed from the string set.
* A successful test will have no errors and no 'lost' messages in the string set.
 */

const (
	appName       = "test-kubemq-stream-AB"
	kubemqAddress = "kubemq-cluster.kubemq.svc.cluster.local"
	kubemqPort    = 50000
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
		log.Panicf("ERROR: MessageSet.add detected dupe message, messageID: %s", messageID)
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

func main() {
	log.Print(appName + ".Begin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// queuesClientA will send messages to channelB and recv messages from channelA.
	queuesClientAConnectHandler := func(msg string) {
		log.Printf("CONNECT-A: msg: %s", msg)
	}
	queuesClientAErrorHandler := func(err error) {
		log.Panicf("ERROR-A: err: %v", err)
	}
	log.Print("Creating queuesClientA")
	queuesClientA, err := kubemq_queues_stream.NewQueuesStreamClient(ctx,
		kubemq_queues_stream.WithAddress(kubemqAddress, kubemqPort),
		kubemq_queues_stream.WithClientId("client-A"),
		kubemq_queues_stream.WithCheckConnection(true),
		kubemq_queues_stream.WithAutoReconnect(true),
		kubemq_queues_stream.WithConnectionNotificationFunc(queuesClientAConnectHandler))
	if err != nil {
		log.Panicf("ERROR-A: NewQueuesStreamClient failed, err: %v", err)
	}
	defer func() {
		err := queuesClientA.Close()
		if err != nil {
			log.Panicf("ERROR-A: QueuesStreamClient.Close failed, err: %v", err)
		}
	}()
	log.Print("Created queuesClientA")
	// queuesClientB will recv messages from channelB and echo/send messages back to channelA.
	queuesClientBConnectHandler := func(msg string) {
		log.Printf("CONNECT-B: msg: %s", msg)
	}
	queuesClientBErrorHandler := func(err error) {
		log.Panicf("ERROR-B: err: %v", err)
	}
	log.Print("Creating queuesClientB")
	queuesClientB, err := kubemq_queues_stream.NewQueuesStreamClient(ctx,
		kubemq_queues_stream.WithAddress(kubemqAddress, kubemqPort),
		kubemq_queues_stream.WithClientId("client-B"),
		kubemq_queues_stream.WithCheckConnection(true),
		kubemq_queues_stream.WithAutoReconnect(true),
		kubemq_queues_stream.WithConnectionNotificationFunc(queuesClientBConnectHandler))
	if err != nil {
		log.Panicf("ERROR-B: NewQueuesStreamClient failed, err: %v", err)
	}
	defer func() {
		err := queuesClientB.Close()
		if err != nil {
			log.Panicf("ERROR-B: QueuesStreamClient.Close failed, err: %v", err)
		}
	}()
	log.Print("Created queuesClientB")
	// messageSet will track queuesClientA send/recv messages.
	messageSet := stringSet{}
	messageSet.init()
	channelA := "test.kubemq.stream.A"
	channelB := "test.kubemq.stream.B"
	// Start queuesClientA receive and print routine.
	// Receives max 100 messages per second.
	log.Print("Running queuesClientA.Poll loop")
	go func() {
		pollRequest := kubemq_queues_stream.NewPollRequest().
			SetChannel(channelA).
			SetMaxItems(100).
			SetWaitTimeout(1 * 1000).
			//SetAutoAck(true).
			SetOnErrorFunc(queuesClientAErrorHandler)
		for {
			pollResponse, err := queuesClientA.Poll(ctx, pollRequest)
			if err != nil {
				log.Panicf("ERROR-A: QueuesStreamClient.Poll failed, err: %v", err)
			}
			if pollResponse.HasMessages() {
				for _, message := range pollResponse.Messages {
					messageString := string(message.Body)
					remainderCount := messageSet.remove(message.MessageID)
					log.Printf("RECV-A: MessageID: %s, Body: %s, Remainder: %d", message.MessageID, messageString, remainderCount)
					// Send message Ack.
					if err := message.Ack(); err != nil {
						log.Panicf("ERROR-A: Message.Ack failed, err: %v", err)
					} else {
						log.Printf("RECV-A: MessageID: %s, Body: %s, Remainder: %d, Ack", message.MessageID, messageString, remainderCount)
					}
				}
			} else {
				log.Printf("RECV-A: No messages")
			}
		}
	}()
	// Start queuesClientB receive and send/echo routine.
	// Receives max 100 messages per second.
	log.Print("Running queuesClientB.Poll loop")
	go func() {
		pollRequest := kubemq_queues_stream.NewPollRequest().
			SetChannel(channelB).
			SetMaxItems(100).
			SetWaitTimeout(1 * 1000).
			//SetAutoAck(true).
			SetOnErrorFunc(queuesClientBErrorHandler)
		for {
			pollResponse, err := queuesClientB.Poll(ctx, pollRequest)
			if err != nil {
				log.Panicf("ERROR-B: QueuesStreamClient.Poll failed, err: %v", err)
			}
			if pollResponse.HasMessages() {
				for _, message := range pollResponse.Messages {
					messageString := string(message.Body)
					log.Printf("RECV-B: MessageID: %s, Body: %s", message.MessageID, messageString)
					// ReQueue message to channelA.
					// This doesn't work with SetAutoAck(true).
					if err := message.ReQueue(channelA); err != nil {
						log.Panicf("ERROR-B: Message.ReQueue failed, err: %v", err)
					} else {
						log.Printf("RECV-B: MessageID: %s, Body: %s, ReQueue", message.MessageID, messageString)
					}
					/*
						// Send messge back to channelA without ReQueue.
						// This works with SetAutoAck(true).
						sendResult, err := queuesClientB.Send(ctx, kubemq_queues_stream.NewQueueMessage().
							SetId(message.MessageID).
							SetChannel(channelA).
							SetBody(message.Body))
						if err != nil {
							log.Panicf("ERROR-B: QueuesStreamClient.Send failed, err: %v", err)
						}
						for _, result := range sendResult.Results {
							log.Printf("SEND-B: MessageID: %s, Body: %s", result.MessageID, messageString)
						}
					*/
				}
			} else {
				log.Printf("RECV-B: No messages")
			}
		}
	}()
	// Start queuesClientA send routine.
	// Sends 20 messages per second.
	wg := new(sync.WaitGroup)
	wg.Add(1)
	log.Print("Running queuesClientA.Send loop")
	go func() {
		for i := 1; i <= 10000; i++ {
			messageID := nuid.New().Next()
			messageSet.add(messageID)
			message := fmt.Sprintf("test_message_%d", i)
			sendResult, err := queuesClientA.Send(ctx, kubemq_queues_stream.NewQueueMessage().
				SetId(messageID).
				SetChannel(channelB).
				SetBody([]byte(message)))
			if err != nil {
				log.Panicf("ERROR-A: QueuesStreamClient.Send failed, err: %v", err)
			}
			for _, result := range sendResult.Results {
				log.Printf("SEND-A: MessageID: %s, Body: %s", result.MessageID, message)
			}
			time.Sleep(50 * time.Millisecond)
		}
		log.Print("Ended queuesClientA.Send loop")
		time.Sleep(10 * time.Second)
		messageSet.print()
		lostCount := messageSet.count()
		if 0 != lostCount {
			log.Panicf("ERROR: Lost Messge Count: %d", lostCount)
		}
		//wg.Done() // Don't let app exit.
	}()
	log.Print("Waiting on wg.Done")
	wg.Wait()
	log.Print(appName + ".End")
}
