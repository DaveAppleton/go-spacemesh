package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spacemeshos/go-spacemesh/api/pb"
	"github.com/spacemeshos/go-spacemesh/log"

	"google.golang.org/grpc"
)

const execPathLabel = "executable-path"

// TODO: this should be a util function
// Contains tells whether a contains x.
// if it does it returns it's index otherwise -1
func Contains(a []string, x string) int {
	for ind, n := range a {
		if strings.Contains(n, x) {
			return ind
		}
	}

	return -1
}

// Harness fully encapsulates an active node server process
// along with client connection and full api, created for rpc integration
// tests and may be used for any other purpose.
type Harness struct {
	server *server
	conn   *grpc.ClientConn
	pb.SpacemeshServiceClient
}

func NewHarnessDefaultServerConfig(args []string) (*Harness, error) {
	// same as in suite's yaml file
	// find executable path label in args
	execPathInd := Contains(args, execPathLabel)
	if execPathInd == -1 {
		return nil, fmt.Errorf("could not find executable path in arguments")
	}
	// next will be exec path value
	execPath := args[execPathInd+1]
	// remove executable path label and value
	args = append(args[:execPathInd], args[execPathInd+2:]...)
	// set servers' configuration
	cfg, errCfg := DefaultConfig(execPath)
	if errCfg != nil {
		return nil, fmt.Errorf("failed to build mock node: %v", errCfg)
	}
	return NewHarness(cfg, args)
}

// NewHarness creates and initializes a new instance of Harness.
func NewHarness(cfg *ServerConfig, args []string) (*Harness, error) {
	log.Info("Starting harness")
	server, err := newServer(cfg)
	if err != nil {
		return nil, err
	}

	// Spawn a new mockNode server process.
	log.Info("harness passing the following arguments: %v", args)
	log.Info("Full node server start listening on: %v", server.cfg.rpcListen)
	if err := server.start(args); err != nil {
		log.Error("Full node ERROR listening on: %v", server.cfg.rpcListen)
		return nil, err
	}

	h := &Harness{
		server: server,
	}

	return h, nil
}

func main() {
	log.JSONLog(true)
	// os.Args[0] contains the current process path
	h, err := NewHarnessDefaultServerConfig(os.Args[1:])
	if err != nil {
		log.With().Error("An error has occurred while generating a new harness: ", log.Err(err))
	}
	// listen on error channel, quit when process stops
	go func() {
		for {
			select {
			case errMsg := <-h.server.errChan:
				log.With().Error("harness received an err from subprocess: ", log.Err(errMsg))
			case <-h.server.quit:
				log.Info("harness got quit signal from subprocess")
				return
			}
		}
	}()
	// a dummy server so the main process won't be terminated before the tests are done running
	srv := &http.Server{Addr: ":6060"}
	defer func() {
		if err := srv.Shutdown(context.TODO()); err != nil {
			log.Error("cannot shutdown http server: ", err)
		}
	}()

	err = srv.ListenAndServe()
	if err != nil {
		log.Error("cannot start http server: ", err)
	}
}
