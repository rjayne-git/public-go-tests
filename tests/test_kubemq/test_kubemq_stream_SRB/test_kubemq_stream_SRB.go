package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	kubemq "github.com/kubemq-io/kubemq-go"
	nuid "github.com/nats-io/nuid"
)

/*
* This code uses a modified version of (https://github.com/kubemq-io/go-sdk-cookbook/blob/main/queues/stream/main.go).
* In this test scenario there are 2 message queues (channelA, channelB) and 3 queue stream clients (senderA, receiverA, queuesClientB).
* senderA sends messages to channelB. Those messageIDs are added to a string set.
* queuesClientB receives messages from channelB and echos/resends them to channelA (via resend).
* receiverA receives messages from channelA. Those messageIDs are removed from the string set.
* A successful test will have no errors and no 'lost' messages in the string set.
 */

const (
	appName       = "test-kubemq-stream-SRB"
	kubemqAddress = "kubemq-cluster-grpc.kubemq.svc.local"
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
	// senderA will send messages to channelB.
	senderA, err := kubemq.NewQueuesStreamClient(ctx,
		kubemq.WithAddress(kubemqAddress, kubemqPort),
		kubemq.WithClientId("sender-A"),
		kubemq.WithTransportType(kubemq.TransportTypeGRPC))
	if err != nil {
		log.Panicf("ERROR-S-A: NewQueuesStreamClient failed, err: %v", err)
	}
	defer func() {
		err := senderA.Close()
		if err != nil {
			log.Panicf("ERROR-S-A: QueuesStreamClient.Close failed, err: %v", err)
		}
	}()
	log.Print("Created senderA")
	// receiverA will recv messages from channelA.
	receiverA, err := kubemq.NewQueuesStreamClient(ctx,
		kubemq.WithAddress(kubemqAddress, kubemqPort),
		kubemq.WithClientId("receiver-A"),
		kubemq.WithTransportType(kubemq.TransportTypeGRPC))
	if err != nil {
		log.Panicf("ERROR-R-A: NewQueuesStreamClient failed, err: %v", err)
	}
	defer func() {
		err := receiverA.Close()
		if err != nil {
			log.Panicf("ERROR-R-A: QueuesStreamClient.Close failed, err: %v", err)
		}
	}()
	log.Print("Created receiverA")
	// queuesClientB will recv messages from channelB and echo/resend messages back to channelA.
	queuesClientB, err := kubemq.NewQueuesStreamClient(ctx,
		kubemq.WithAddress(kubemqAddress, kubemqPort),
		kubemq.WithClientId("client-B"),
		kubemq.WithTransportType(kubemq.TransportTypeGRPC))
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
	// Start receiverA receive and print routine.
	log.Print("Running receiverA.TransactionStream")
	doneA, err := receiverA.TransactionStream(ctx, &kubemq.QueueTransactionMessageRequest{
		ClientID:          "receiver-A",
		Channel:           channelA,
		VisibilitySeconds: 10,
		WaitTimeSeconds:   10,
	}, func(response *kubemq.QueueTransactionMessageResponse, err error) {
		if err != nil {
			log.Printf("ERROR-R-A: QueueTransactionMessageResponse failed, err: %v", err)
		} else {
			remainderCount := messageSet.remove(response.Message.MessageID)
			log.Printf("RECV-A: MessageID: %s, Body: %s, Remainder: %d, Ack", response.Message.MessageID, string(response.Message.Body), remainderCount)
			err = response.Ack()
			if err != nil {
				log.Panicf("ERROR-R-A: QueueTransactionMessageResponse.Ack failed, err: %v", err)
			}
		}
	})
	if err != nil {
		log.Panicf("ERROR-R-A: TransactionStream failed, err: %v", err)
	}
	defer func() { doneA <- struct{}{} }()
	// Start queuesClientB receive and echo/resend routine.
	log.Print("Running queuesClientB.TransactionStream")
	doneB, err := queuesClientB.TransactionStream(ctx, &kubemq.QueueTransactionMessageRequest{
		ClientID:          "client-B",
		Channel:           channelB,
		VisibilitySeconds: 10,
		WaitTimeSeconds:   10,
	}, func(response *kubemq.QueueTransactionMessageResponse, err error) {
		if err != nil {
			log.Printf("ERROR-B: QueueTransactionMessageResponse failed, err: %v", err)
		} else {
			message := string(response.Message.Body)
			log.Printf("RECV-B: MessageID: %s, Body: %s, Ack", response.Message.MessageID, message)
			err = response.Resend(channelA) // Also performs Ack.
			if err != nil {
				log.Panicf("ERROR-B: QueueTransactionMessageResponse.Resend failed, err: %v", err)
			}
			log.Printf("RSND-B: MessageID: %s, Body: %s", response.Message.MessageID, message)
		}
	})
	if err != nil {
		log.Panicf("ERROR-B: TransactionStream failed, err: %v", err)
	}
	defer func() { doneB <- struct{}{} }()
	// Start senderA send routine.
	wg := new(sync.WaitGroup)
	wg.Add(1)
	log.Print("Running senderA.Send loop")
	go func() {
		for i := 1; i <= 10000; i++ {
			messageID := nuid.New().Next()
			messageSet.add(messageID)
			message := fmt.Sprintf("test_message_%d", i)
			sendResult, err := senderA.Send(ctx, kubemq.NewQueueMessage().
				SetId(messageID).
				SetChannel(channelB).
				SetBody([]byte(message)))
			if err != nil {
				log.Panicf("ERROR-S-A: QueuesStreamClient.Send failed, err: %v", err)
			}
			log.Printf("SEND-A: MessageID: %s, Body: %s", sendResult.MessageID, message)
			time.Sleep(100 * time.Millisecond)
		}
		log.Print("Ended senderA.Send loop")
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
