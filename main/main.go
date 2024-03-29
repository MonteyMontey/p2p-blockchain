package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	golog "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-crypto"
	"github.com/libp2p/go-libp2p-host"
	"github.com/libp2p/go-libp2p-net"
	"github.com/libp2p/go-libp2p-peer"
	pstore "github.com/libp2p/go-libp2p-peerstore"
	ma "github.com/multiformats/go-multiaddr"
	gologging "github.com/whyrusleeping/go-logging"
)

var mutex = &sync.Mutex{}

var Blockchain []Block

type Block struct {
	Index     int
	Timestamp string
	BPM       int
	Hash      string
	PrevHash  string
}

func main() {
	t := time.Now()
	genesisBlock := Block{}
	genesisBlock = Block{0, t.String(), 0, calculateHash(genesisBlock), ""}

	Blockchain = append(Blockchain, genesisBlock)

	// LibP2P code uses golog to log messages. We can control the verbosity level for all loggers with:
	golog.SetAllLoggers(gologging.INFO) // Change to DEBUG for extra info

	// Parse options from the command line
	listenF := flag.Int("l", 0, "wait for incoming connections")
	target := flag.String("d", "", "target peer to dial")
	secio := flag.Bool("secio", false, "enable secio")
	flag.Parse()

	if *listenF == 0 {
		log.Fatal("Please provide a port to bind on with -l")
	}

	// Make a host that listens on the given multiaddress (multiaddress something like "/ip4/127.0.0.1/udp/1234")
	p2pHost, err := makeBasicHost(*listenF, *secio)
	if err != nil {
		log.Fatal(err)
	}

	// Set a stream handler on host A. /p2p/1.0.0 is a user-defined protocol name.
	p2pHost.SetStreamHandler("/p2p/1.0.0", handleStream)

	if *target == "" {
		log.Println("listening for connections")
		select {}

	} else {

		/*
		#############################################################################
		the following code does extract targetAddr & peer ID from multiaddress (in a overly complicated way as it seems),

		multiaddress: /ip4/127.0.0.1/tcp/10000/ipfs/QmVvoaVEgoQNwAk7wXi8KFi7B3SECTKnkeDZ2Cy9h6x1yX
		peer ID: QmVvoaVEgoQNwAk7wXi8KFi7B3SECTKnkeDZ2Cy9h6x1yX
		targetAddress: /ip4/127.0.0.1/tcp/10000
		*/

		ipfsaddr, err := ma.NewMultiaddr(*target)
		if err != nil {
			log.Fatalln(err)
		}
		pid, err := ipfsaddr.ValueForProtocol(ma.P_IPFS)
		if err != nil {
			log.Fatalln(err)
		}
		peerid, err := peer.IDB58Decode(pid)
		if err != nil {
			log.Fatalln(err)
		}

		targetPeerAddr, _ := ma.NewMultiaddr(fmt.Sprintf("/ipfs/%s", peer.IDB58Encode(peerid)))
		targetAddr := ipfsaddr.Decapsulate(targetPeerAddr)

		/* ######################################################################## */


		// add peer ID & target address to peerstore so LibP2P knows how to contact it
		p2pHost.Peerstore().AddAddr(peerid, targetAddr, pstore.PermanentAddrTTL)

		log.Println("opening stream")
		stream, err := p2pHost.NewStream(context.Background(), peerid, "/p2p/1.0.0")
		if err != nil {
			log.Fatalln(err)
		}

		// Create a buffered stream so that read and writes are non blocking.
		rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))

		go writeData(rw)
		go readData(rw)

		select {} // hang forever
	}
}

/* P2P related stuff */

// makeBasicHost creates a LibP2P host with a random peer ID listening on the given multiaddress
func makeBasicHost(listenPort int, secio bool) (host.Host, error) {

	// Generate a key pair for this host. We will use it to obtain a valid host ID.
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, rand.Reader)
	if err != nil {
		return nil, err
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", listenPort)),
		libp2p.Identity(priv),
	}

	basicHost, err := libp2p.New(context.Background(), opts...)
	if err != nil {
		return nil, err
	}

	// Build host multiaddress
	hostAddr, _ := ma.NewMultiaddr(fmt.Sprintf("/ipfs/%s", basicHost.ID().Pretty()))

	// build full multiaddress to reach this host by encapsulating both addresses:
	addr := basicHost.Addrs()[0]
	fullAddr := addr.Encapsulate(hostAddr)
	log.Printf("I am %s\n", fullAddr)

	return basicHost, nil
}

func handleStream(s net.Stream) {
	log.Println("Got a new stream!")

	// Create a buffer stream for non blocking read and write.
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	go readData(rw)
	go writeData(rw)

	// stream 's' will stay open until you close it (or the other side closes it).
}

func readData(rw *bufio.ReadWriter) {

	for {
		str, err := rw.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		if str == "" {
			return
		}
		if str != "\n" {

			chain := make([]Block, 0)
			if err := json.Unmarshal([]byte(str), &chain); err != nil {
				log.Fatal(err)
			}

			mutex.Lock()
			if len(chain) > len(Blockchain) {
				Blockchain = chain
				bytes, err := json.MarshalIndent(Blockchain, "", "  ")
				if err != nil {

					log.Fatal(err)
				}
				// Green console color: 	\x1b[32m
				// Reset console color: 	\x1b[0m
				fmt.Printf("\x1b[32m%s\x1b[0m> ", string(bytes))
			}
			mutex.Unlock()
		}
	}
}


func writeData(rw *bufio.ReadWriter) {

	// updates other peers every some seconds
	go func() {
		for {
			time.Sleep(5 * time.Second)
			mutex.Lock()
			bytes, err := json.Marshal(Blockchain)
			if err != nil {
				log.Println(err)
			}
			mutex.Unlock()

			mutex.Lock()
			_, _ = rw.WriteString(fmt.Sprintf("%s\n", string(bytes)))
			_ = rw.Flush()
			mutex.Unlock()

		}
	}()

	stdReader := bufio.NewReader(os.Stdin)

	// waits for input and sends it
	for {
		fmt.Print("> ")
		sendData, err := stdReader.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}

		sendData = strings.Replace(sendData, "\n", "", -1)
		bpm, err := strconv.Atoi(sendData)
		if err != nil {
			log.Fatal(err)
		}
		newBlock := generateBlock(Blockchain[len(Blockchain)-1], bpm)

		if isBlockValid(newBlock, Blockchain[len(Blockchain)-1]) {
			mutex.Lock()
			Blockchain = append(Blockchain, newBlock)
			mutex.Unlock()
		}

		bytes, err := json.Marshal(Blockchain)
		if err != nil {
			log.Println(err)
		}

		spew.Dump(Blockchain)

		mutex.Lock()
		_, _ = rw.WriteString(fmt.Sprintf("%s\n", string(bytes)))
		_ = rw.Flush()
		mutex.Unlock()
	}

}

/* Blockchain related */

func isBlockValid(newBlock, oldBlock Block) bool {
	if oldBlock.Index+1 != newBlock.Index {
		return false
	}
	if oldBlock.Hash != newBlock.PrevHash {
		return false
	}
	if calculateHash(newBlock) != newBlock.Hash {
		return false
	}
	return true
}

func calculateHash(block Block) string {
	record := strconv.Itoa(block.Index) + block.Timestamp + strconv.Itoa(block.BPM) + block.PrevHash
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}

func generateBlock(oldBlock Block, BPM int) Block {
	var newBlock Block
	newBlock.Index = oldBlock.Index + 1
	newBlock.Timestamp = time.Now().String()
	newBlock.BPM = BPM
	newBlock.PrevHash = oldBlock.Hash
	newBlock.Hash = calculateHash(newBlock)
	return newBlock
}
