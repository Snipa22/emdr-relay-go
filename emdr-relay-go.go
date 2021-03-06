package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	couchbase "github.com/couchbase/go-couchbase"
	cache "github.com/gtaylor/emdr-relay-go/cache"
	zmq "github.com/pebbe/zmq4"
	"hash"
	"hash/fnv"
	"io/ioutil"
	"encoding/json"
	"os"
	"time"
	"unsafe"
)

// The presence of the cache value is all we need, so keep this super simple.
type CacheValue struct {
	found bool
}

type EMDRMsg struct {
	ResultType string `json:"resultType"`
	Version    string `json:"version"`
	UploadKeys []struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	} `json:"uploadKeys"`
	Generator struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"generator"`
	CurrentTime time.Time `json:"currentTime"`
	Columns     []string  `json:"columns"`
	Rowsets     []struct {
		GeneratedAt time.Time `json:"generatedAt"`
		RegionID    int       `json:"regionID"`
		TypeID      int       `json:"typeID"`
		Rows        []struct {
			Num0  int       `json:"0"`
			Num1  int       `json:"1"`
			Num2  int       `json:"2"`
			Num3  int64     `json:"3"`
			Num4  int       `json:"4"`
			Num5  int       `json:"5"`
			Num6  bool      `json:"6"`
			Num7  time.Time `json:"7"`
			Num8  int       `json:"8"`
			Num9  int       `json:"9"`
			Num10 int       `json:"10"`
		} `json:"rows"`
	} `json:"rowsets"`
}

type Configuration struct {
	URI            string   `json:"URI"`
	Cluster        string   `json:"Cluster"`
	Bucket         string   `json:"Bucket"`
	RelayList      []string `json:"RelayList"`
	CouchbaseCache bool     `json:"CouchbaseCache"`
}

type EMDRDoc struct {
	Region     int `json:"region"`
	ItemID     int `json:"ItemID"`
	InsertTime int `json:"InsertTime"`
	UploadKeys []struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	ResultType string `json:"resultType"`
}

//Calculate the size (in bytes) of our struct.
const cache_value_size = int64(unsafe.Sizeof(CacheValue{}))

// Determines the max cache size, in bytes.
const cache_size_limit = cache_value_size * 1000

// Satisfies the Value interface.
func (self *CacheValue) Size() int {
	return int(cache_value_size)
}

func periodic_exiter() {
	// We exit periodically so that the process supervisor can restart us.
	// This helps recover from some edge cases where connections to the
	// announcers aren't picked back up.
	// Currently hardcoded to every 12 hours.
	ticker := time.NewTicker(12 * 3600 * time.Second)
	for {
		select {
		case <-ticker.C:
			println("Exiting so we can auto-restart.")
			os.Exit(0)
		}
	}
}

func main() {
	println("=====================[ emdr-relay-go ]=====================")
	println("Starting emdr-relay-go 1.1...")
	cache := cache.NewLRUCache(cache_size_limit)

	file, _ := os.Open("config.json")
	decoder := json.NewDecoder(file)
	configuration := Configuration{}
	err := decoder.Decode(&configuration)
	if err != nil {
		fmt.Println("error:", err)
	}

	b, err := couchbase.GetBucket(configuration.URI, configuration.Cluster, configuration.Bucket)
	receiver, _ := zmq.NewSocket(zmq.SUB)
	for _, relay := range Configuration.RelayList {
		receiver.Connect(relay)
	}
	receiver.SetSubscribe("")
	defer receiver.Close()

	sender, _ := zmq.NewSocket(zmq.PUB)
	sender.Bind("tcp://*:8050")
	defer sender.Close()

	println("Listening on port 8050.")
	println("===========================================================")
	//  Ensure subscriber connection has time to complete
	time.Sleep(time.Second)

	// We auto-exit every number of hours so we can recover from some
	// weird edge case conditions that disrupt the network. They're not common,
	// but we'll do this to be absolutely sure.
	go periodic_exiter()

	for {
		msg, zmq_err := receiver.Recv(0)
		if zmq_err != nil {
			println("RECV ERROR:", zmq_err.Error())
		}

		var h hash.Hash = fnv.New32()
		h.Write([]byte(msg))

		checksum := h.Sum([]byte{})
		cache_key := fmt.Sprintf("%x", checksum)

		cache_item, cache_hit := cache.Get(cache_key)
		if cache_hit {
			// We've already seen this before, ignore it.
			continue
		}

		// At this point, we know we've encountered a message we haven't
		// seen in the recent past.
		cache_item = &CacheValue{found: true}
		// Insert the cache entry to prevent future re-sends of this message.
		cache.Set(cache_key, cache_item)

		// A cache miss means that the incoming message is not a dupe.
		// Send the message to subscribers.
		sender.Send(msg, 0)
		if Configuration.CouchbaseCache == false {
			continue
		}
		var m EMDRMsg
		decoded, err := ZlibDecode(msg)
		if err != nil {
			log.Fatal(err)
		}
		err := json.Unmarshal(decoded, &m)
		for _, element := range m.Rowsets {
			if element.GeneratedAt.Unix() >= int32(time.Now().Unix())-3600 {
				val := EMDRDoc{element.RegionID, element.TypeID, int32(time.Now().Unix()), m.UploadKeys, m.ResultType}
				var buffer bytes.Buffer
				region_string, _ := strconv.Itoa(element.RegionID)
				type_string, _ := strconv.Itoa(element.TypeID)
				buffer.WriteString(region_string)
				buffer.WriteString("-")
				buffer.WriteString(type_string)
				buffer.WriteString("-")
				buffer.WriteString(m.ResultType)
				b.Set(buffer.String(), val)
			}
		}
	}
}

func ZlibDecode(encoded string) (decoded []byte, err error) {
	b := bytes.NewBufferString(encoded)
	pipeline, err := zlib.NewReader(b)

	if err == nil {
		defer pipeline.Close()
		decoded, err = ioutil.ReadAll(pipeline)
	}

	return
}
