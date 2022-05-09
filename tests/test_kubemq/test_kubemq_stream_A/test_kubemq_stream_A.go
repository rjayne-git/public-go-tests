package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	kubemq "github.com/kubemq-io/kubemq-go"
	nuid "github.com/nats-io/nuid"
)

/*
* This code uses a modified version of (https://github.com/kubemq-io/go-sdk-cookbook/blob/main/queues/stream/main.go).
* In this test scenario there is 1 message queue (channelA) and 1 queue stream client (queuesClientA).
* queuesClientA sends messages to channelA. Those messageIDs are added to a string set.
* queuesClientA receives messages from channelA.  Those messageIDs are removed from the string set.
* A successful test will have no errors and no 'lost' messages in the string set.
 */

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

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// queuesClientA will send messages to channelB and recv messages from channelA.
	queuesClientA, err := kubemq.NewQueuesStreamClient(ctx,
		kubemq.WithAddress("localhost", 50000),
		kubemq.WithClientId("test-kubemq-stream-A"),
		kubemq.WithTransportType(kubemq.TransportTypeGRPC))
	if err != nil {
		log.Fatalf("ERROR-A: NewQueuesStreamClient failed, err: %v", err)
	}
	defer func() {
		err := queuesClientA.Close()
		if err != nil {
			log.Fatalf("ERROR-A: QueuesStreamClient.Close failed, err: %v", err)
		}
	}()
	// messageSet will track queuesClientA send/recv messages.
	messageSet := stringSet{}
	messageSet.init()
	channelA := "test.kubemq.stream.A"
	// Start queuesClientA receive and print routine.
	doneA, err := queuesClientA.TransactionStream(ctx, &kubemq.QueueTransactionMessageRequest{
		ClientID:          "test-kubemq-stream-receiver-A",
		Channel:           channelA,
		VisibilitySeconds: 10,
		WaitTimeSeconds:   10,
	}, func(response *kubemq.QueueTransactionMessageResponse, err error) {
		if err != nil {
			log.Printf("ERROR-A: QueueTransactionMessageResponse failed, err: %v", err)
		} else {
			remainderCount := messageSet.remove(response.Message.MessageID)
			log.Printf("RECV-A: MessageID: %s, Body: %s, Remainder: %d, Ack", response.Message.MessageID, string(response.Message.Body), remainderCount)
			err = response.Ack()
			if err != nil {
				log.Fatalf("ERROR-A: QueueTransactionMessageResponse.Ack failed, err: %v", err)
			}
		}
	})
	if err != nil {
		log.Fatalf("ERROR-A: TransactionStream failed, err: %v", err)
	}
	defer func() { doneA <- struct{}{} }()
	// Start queuesClientA send routine.
	var testDoneChan = make(chan struct{})
	go func() {
		for i := 1; i <= 10000; i++ {
			messageID := nuid.New().Next()
			messageSet.add(messageID)
			message := fmt.Sprintf("test_message_%d", i)
			sendResult, err := queuesClientA.Send(ctx, kubemq.NewQueueMessage().
				SetId(messageID).
				SetChannel(channelA).
				SetBody([]byte(message)))
			if err != nil {
				log.Fatalf("ERROR-A: QueuesStreamClient.Send failed, err: %v", err)
			}
			log.Printf("SEND-A: MessageID: %s, Body: %s", sendResult.MessageID, message)
			time.Sleep(100 * time.Millisecond)
		}
		close(testDoneChan)
	}()
	var gracefulShutdown = make(chan os.Signal, 1)
	signal.Notify(gracefulShutdown, syscall.SIGTERM)
	signal.Notify(gracefulShutdown, syscall.SIGINT)
	signal.Notify(gracefulShutdown, syscall.SIGQUIT)
	select {
	case <-gracefulShutdown:
	case <-testDoneChan:
	}
	messageSet.print()
	lostCount := messageSet.count()
	if 0 != lostCount {
		log.Fatalf("ERROR: Lost Messge Count: %d", lostCount)
	}
}
