package main

import (
	"context"
	"flag"
	"log"
	"os"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	nuid "github.com/nats-io/nuid"
)

const (
	appName         = "test-nats-kv"
	natsUrlLocal    = "nats://127.0.0.1:4222"
	natsUrlMinikube = "nats://127.0.0.1:4222"
	natsUrlGKE      = "nats://nats.default.svc.cluster.local:4222"
	connectionName  = "TestConnectionKV"
	subjectWild     = "test.nats.*"
	kvBucket        = appName
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
	opts := []nats.Option{}
	opts = setupConnectionOptions(opts, connectionName)
	connection, err := nats.Connect(natsUrl, opts...)
	if err != nil {
		log.Panicf("ERROR: nats.Connect failed, err: %v", err)
	}
	defer connection.Close()
	jetstreamContext, err := connection.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		log.Panicf("ERROR: nats.Conn.JetStream failed, err: %v", err)
	}
	log.Print("Created connection and jetstreamContext")
	// keySet will track keys.
	keySet := stringSet{}
	keySet.init()
	wg := new(sync.WaitGroup)
	recvwg := new(sync.WaitGroup)
	// Create kv store.
	kv, err := jetstreamContext.CreateKeyValue(&nats.KeyValueConfig{Bucket: kvBucket, History: 1})
	if err != nil {
		log.Panicf("ERROR: nats.JetStreamContext.CreateKeyValue failed, err: %v", err)
	}
	// Start key watcher routine.
	wg.Add(1)
	recvwg.Add(1)
	log.Print("Running key recv routine")
	go func() {
		defer wg.Done()
		// Create key watcher.
		wopts := []nats.WatchOpt{}
		watcher, err := kv.WatchAll(wopts...)
		if err != nil {
			log.Panicf("ERROR: nats.KeyValue.WatchAll failed, err: %v", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case kve := <-watcher.Updates():
				if kve != nil {
					log.Printf("RECV: key: %s", kve.Key())
					keySet.remove(kve.Key())
				} else {
					// 'nil' means that the initial data that was already present has been processed.
					// What comes after that is new data.
					log.Printf("RECV: key: nil")
					recvwg.Done()
				}
			}
		}
	}()
	// Wait for watcher to be ready for new data.
	recvwg.Wait()
	// Start key modifier routine.
	wg.Add(1)
	log.Print("Running key send loop")
	go func() {
		defer cancel()
		for i := 1; i <= 1000; i++ {
			key := getUniqueId()
			keySet.add(key)
			if _, err := kv.PutString(key, key); err != nil {
				log.Panicf("ERROR: nats.KeyValue.PutString failed, err: %v", err)
			}
			log.Printf("SEND: key: %s", key)
			time.Sleep(20 * time.Millisecond)
		}
		waitWithReason(time.Second, 10, "Giving time for all updates to arrive")
		keySet.print()
		lostCount := keySet.count()
		if 0 != lostCount {
			log.Panicf("ERROR: Lost Key Count: %d", lostCount)
		}
		wg.Done() // Don't let app exit.
	}()
	log.Print("Waiting on wg.Done")
	wg.Wait()
	// Delete kv store.
	if err := jetstreamContext.DeleteKeyValue(kvBucket); err != nil {
		log.Panicf("ERROR: nats.JetStreamContext.DeleteKeyValue failed, err: %v", err)
	}
	log.Print(appName + " End")
}
