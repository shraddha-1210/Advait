// Command oracle manages the simulated FX oracle key.
//
//	oracle keygen <dir>                         write <dir>/oracle.key and oracle.pub
//	oracle sign <keyfile> <seq> <rateMicros>    print a signed attestation as JSON
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/advait/pvp-settlement/gateway/internal/oracle"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "keygen":
		if len(os.Args) != 3 {
			usage()
		}
		pub, err := oracle.Keygen(os.Args[2])
		if err != nil {
			fail(err)
		}
		fmt.Println(pub)
	case "sign":
		if len(os.Args) != 5 {
			usage()
		}
		s, err := oracle.Load(os.Args[2])
		if err != nil {
			fail(err)
		}
		seq, err1 := strconv.ParseInt(os.Args[3], 10, 64)
		rate, err2 := strconv.ParseInt(os.Args[4], 10, 64)
		if err1 != nil || err2 != nil {
			usage()
		}
		out, _ := json.Marshal(s.Sign(seq, rate, time.Now()))
		fmt.Println(string(out))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: oracle keygen <dir> | oracle sign <keyfile> <seq> <rateMicros>")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
