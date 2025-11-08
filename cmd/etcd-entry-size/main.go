package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	gogo_proto "github.com/gogo/protobuf/proto"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// estimate how much storage a KeyValue proto marshals to. This is a
// reasonable approximation of the on-disk protobuf size etcd stores.
func kvSize(kv *mvccpb.KeyValue) (int, error) {
	if kv == nil {
		return 0, errors.New("nil kv")
	}
	b, err := gogo_proto.Marshal(kv)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func main() {
	endpointsFlag := flag.String("endpoints", "localhost:2379", "comma-separated list of etcd endpoints")
	keyFlag := flag.String("key", "", "key to inspect (required)")
	timeoutFlag := flag.Duration("timeout", 10*time.Second, "dial / request timeout")
	exhaustiveFlag := flag.Bool("exhaustive", false, "if true, scan every revision between first and current (slower) when watch history is unavailable")
	certFileFlag := flag.String("cert-file", "", "client certificate file (PEM)")
	keyFileFlag := flag.String("key-file", "", "client private key file (PEM)")
	caFileFlag := flag.String("ca-file", "", "CA certificate file (PEM) to verify server")
	flag.Parse()

	if *keyFlag == "" {
		fmt.Fprintln(os.Stderr, "--key is required")
		flag.Usage()
		os.Exit(2)
	}

	endpoints := strings.Split(*endpointsFlag, ",")

	// build TLS config if cert/key provided
	var tlsCfg *tls.Config
	if *certFileFlag != "" || *keyFileFlag != "" || *caFileFlag != "" {
		tlsCfg = &tls.Config{}
		// load client cert if provided
		if *certFileFlag != "" || *keyFileFlag != "" {
			cert, err := tls.LoadX509KeyPair(*certFileFlag, *keyFileFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to load client cert/key: %v\n", err)
				os.Exit(1)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		// load CA if provided
		if *caFileFlag != "" {
			caData, err := ioutil.ReadFile(*caFileFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to read ca file: %v\n", err)
				os.Exit(1)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caData) {
				fmt.Fprintln(os.Stderr, "failed to append ca certs")
				os.Exit(1)
			}
			tlsCfg.RootCAs = pool
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	cfg := clientv3.Config{Endpoints: endpoints, DialTimeout: *timeoutFlag}
	if tlsCfg != nil {
		cfg.TLS = tlsCfg
	}

	cli, err := clientv3.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to etcd: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	// Get current value to learn the cluster revision and ensure key exists now
	getResp, err := cli.Get(ctx, *keyFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get failed: %v\n", err)
		os.Exit(1)
	}
	if getResp.Count == 0 {
		fmt.Fprintf(os.Stderr, "key %q not found at current revision %d\n", *keyFlag, getResp.Header.Revision)
		os.Exit(1)
	}

	currentRev := getResp.Header.Revision

	// Binary search to find the first revision the key exists in.
	firstRev, err := findFirstRevision(ctx, cli, *keyFlag, 1, currentRev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to find first revision: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("firstRevision=%d\ncurrentRevision=%d\n", firstRev, currentRev)

	// Try to collect per-revision KeyValues by watching from firstRev.
	totalBytes, seen, watchErr := collectByWatch(ctx, cli, *keyFlag, firstRev, currentRev)
	if watchErr != nil {
		fmt.Fprintf(os.Stderr, "watch-based collection failed: %v\n", watchErr)
		if *exhaustiveFlag {
			fmt.Fprintln(os.Stderr, "falling back to exhaustive per-revision scan")
			totalBytes, seen, err = collectByScan(ctx, cli, *keyFlag, firstRev, currentRev)
			if err != nil {
				fmt.Fprintf(os.Stderr, "exhaustive scan failed: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintln(os.Stderr, "try again with --exhaustive to force a slower per-revision scan (may be large)")
			os.Exit(1)
		}
	}

	fmt.Printf("revisions_count=%d\nestimated_total_bytes=%d\n", len(seen), totalBytes)
}

// findFirstRevision uses binary search over revisions to find the first
// revision where the key exists. This assumes history hasn't been compacted
// below the returned revision; if history is compacted this may return an
// error.
func findFirstRevision(ctx context.Context, cli *clientv3.Client, key string, low, high int64) (int64, error) {
	// guard
	if low < 1 {
		low = 1
	}
	var first int64 = -1
	for low <= high {
		mid := (low + high) / 2
		// use a short-lived context for each probe
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := cli.Get(cctx, key, clientv3.WithRev(mid), clientv3.WithCountOnly())
		cancel()
		if err != nil {
			if err == rpctypes.ErrCompacted {
				low = mid + 1
				continue
			}
			return -1, err
		}
		if resp.Count > 0 {
			first = mid
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	if first == -1 {
		return -1, fmt.Errorf("key not found in revisions range")
	}
	return first, nil
}

// collectByWatch attempts to watch from startRev and capture existing
// historical events up to currentRev. Returns total bytes, map of seen
// revisions to their sizes, and error (non-nil if watch couldn't complete).
func collectByWatch(ctx context.Context, cli *clientv3.Client, key string, startRev, currentRev int64) (int64, map[int64]int, error) {
	out := make(map[int64]int)
	var total int64

	watchCh := cli.Watch(ctx, key, clientv3.WithRev(startRev), clientv3.WithPrevKV())

	for wr := range watchCh {
		if wr.Err() != nil {
			return 0, nil, wr.Err()
		}
		// iterate events
		for _, ev := range wr.Events {
			// Kv may be nil in some events; PrevKv may be set when requested
			candidates := []*mvccpb.KeyValue{}
			if ev.Kv != nil {
				candidates = append(candidates, ev.Kv)
			}
			if ev.PrevKv != nil {
				candidates = append(candidates, ev.PrevKv)
			}
			for _, kv := range candidates {
				rev := kv.ModRevision
				if rev == 0 {
					// fallback to CreateRevision if ModRevision missing
					rev = kv.CreateRevision
				}
				if _, ok := out[rev]; ok {
					continue
				}
				sz, err := kvSize(kv)
				if err != nil {
					return 0, nil, err
				}
				out[rev] = sz
				total += int64(sz)
			}
		}
		if wr.Header.Revision >= currentRev {
			// we've observed the cluster up to currentRev
			break
		}
	}

	return total, out, nil
}

// collectByScan performs an exhaustive per-revision scan between startRev
// and currentRev by issuing Get(key, WithRev(rev)) for each rev. This is
// slower but works when watch history is unavailable due to compaction.
func collectByScan(ctx context.Context, cli *clientv3.Client, key string, startRev, currentRev int64) (int64, map[int64]int, error) {
	out := make(map[int64]int)
	var total int64

	for rev := startRev; rev <= currentRev; rev++ {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := cli.Get(cctx, key, clientv3.WithRev(rev))
		cancel()
		if err != nil {
			return 0, nil, err
		}
		if resp.Count == 0 {
			continue
		}
		// resp.Kvs may contain the key for this revision
		for _, kv := range resp.Kvs {
			r := kv.ModRevision
			if r == 0 {
				r = kv.CreateRevision
			}
			if _, ok := out[r]; ok {
				continue
			}
			sz, err := kvSize(kv)
			if err != nil {
				return 0, nil, err
			}
			out[r] = sz
			total += int64(sz)
		}
	}

	return total, out, nil
}
