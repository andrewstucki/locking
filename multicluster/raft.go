package multicluster

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"time"

	"github.com/andrewstucki/locking"
	"github.com/andrewstucki/locking/raft"
	"github.com/go-logr/logr"
	flag "github.com/spf13/pflag"
	raftv4 "go.etcd.io/raft/v3"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/providers/clusters"
)

func stringToHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

type RaftCluster struct {
	Name           string
	Address        string
	KubeconfigFile string
}

type RaftConfiguration struct {
	Name              string
	Address           string
	CAFile            string
	PrivateKeyFile    string
	CertificateFile   string
	Scheme            *runtime.Scheme
	Peers             []RaftCluster
	ElectionTimeout   time.Duration
	HeartbeatInterval time.Duration
	Logger            logr.Logger
}

func (r RaftConfiguration) validate() error {
	if r.Name == "" {
		return errors.New("name must be specified")
	}
	if r.Address == "" {
		return errors.New("address must be specified")
	}
	if len(r.CAFile) == 0 {
		return errors.New("ca must be specified")
	}
	if len(r.PrivateKeyFile) == 0 {
		return errors.New("private key must be specified")
	}
	if len(r.CertificateFile) == 0 {
		return errors.New("certificate must be specified")
	}
	if len(r.Peers) == 0 {
		return errors.New("peers must be set")
	}

	return nil
}

var (
	cliRaftConfiguration      RaftConfiguration
	cliRaftConfigurationPeers []string
)

func AddRaftConfigurationFlags(set *flag.FlagSet) {
	set.StringVar(&cliRaftConfiguration.Name, "raft-node-name", "", "raft node name")
	set.StringVar(&cliRaftConfiguration.Address, "raft-node-address", "", "raft node address")
	set.StringVar(&cliRaftConfiguration.CAFile, "raft-ca-file", "", "raft ca file")
	set.StringVar(&cliRaftConfiguration.PrivateKeyFile, "raft-private-key-file", "", "raft private key file")
	set.StringVar(&cliRaftConfiguration.CertificateFile, "raft-certificate-file", "", "raft certificate file")
	set.DurationVar(&cliRaftConfiguration.ElectionTimeout, "raft-election-timeout", 10*time.Second, "raft election timeout")
	set.DurationVar(&cliRaftConfiguration.HeartbeatInterval, "raft-heartbeat-interval", 1*time.Second, "raft heartbeat interval")
	set.StringSliceVar(&cliRaftConfigurationPeers, "raft-peers", []string{}, "raft peers")
}

func RaftConfigurationFromFlags() (RaftConfiguration, error) {
	for _, peer := range cliRaftConfigurationPeers {
		cluster, err := peerFromFlag(peer)
		if err != nil {
			return RaftConfiguration{}, err
		}
		cliRaftConfiguration.Peers = append(cliRaftConfiguration.Peers, cluster)
	}

	return cliRaftConfiguration, nil
}

func peerFromFlag(value string) (RaftCluster, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Path == "" {
		return RaftCluster{}, errors.New("format of peer flag is name://address/path/to/kubeconfig")
	}
	return RaftCluster{
		Name:           parsed.Scheme,
		Address:        parsed.Host,
		KubeconfigFile: parsed.Path,
	}, nil
}

func NewRaftRuntimeManager(config RaftConfiguration) (mcmanager.Manager, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}

	raftPeers := []raft.LockerNode{}
	clusterProvider := clusters.New()
	for _, peer := range config.Peers {
		kubeConfig, err := loadKubeconfig(peer.KubeconfigFile)
		if err != nil {
			return nil, err
		}
		c, err := cluster.New(kubeConfig)
		if err != nil {
			return nil, err
		}
		if err := clusterProvider.Add(context.Background(), peer.Name, c, nil); err != nil {
			return nil, err
		}

		raftPeers = append(raftPeers, raft.LockerNode{
			ID:      stringToHash(peer.Name),
			Address: peer.Address,
		})
	}

	caBytes, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, err
	}

	certBytes, err := os.ReadFile(config.CertificateFile)
	if err != nil {
		return nil, err
	}

	keyBytes, err := os.ReadFile(config.PrivateKeyFile)
	if err != nil {
		return nil, err
	}

	raftConfig := raft.LockConfiguration{
		ID:                stringToHash(config.Name),
		Address:           config.Address,
		CA:                caBytes,
		Certificate:       certBytes,
		PrivateKey:        keyBytes,
		Peers:             raftPeers,
		ElectionTimeout:   config.ElectionTimeout,
		HeartbeatInterval: config.HeartbeatInterval,
		Logger:            &raftLogr{logger: config.Logger},
	}

	return newManager(restConfig, clusterProvider, locking.NewRaftLockManager(raftConfig), manager.Options{
		Scheme:         config.Scheme,
		LeaderElection: false,
	})
}

type raftLogr struct {
	logger logr.Logger
}

func (r *raftLogr) Debug(v ...any) {
	r.logger.V(1).Info("DEBUG", v...)
}
func (r *raftLogr) Debugf(format string, v ...any) {
	if format == "" {
		r.logger.V(1).Info("DEBUG", v...)
	} else {
		r.logger.V(1).Info(fmt.Sprintf("[DEBUG] %s", fmt.Sprintf(format, v...)))
	}
}

func (r *raftLogr) Error(v ...any) {
	r.logger.Error(errors.New("an error occurred"), "ERROR", v...)
}
func (r *raftLogr) Errorf(format string, v ...any) {
	if format == "" {
		r.logger.Error(errors.New("an error occurred"), "ERROR", v...)
	} else {
		text := fmt.Sprintf(format, v...)
		r.logger.Error(errors.New(text), text)
	}
}

func (r *raftLogr) Info(v ...any) {
	r.logger.V(0).Info("INFO", v...)
}
func (r *raftLogr) Infof(format string, v ...any) {
	if format == "" {
		r.logger.V(0).Info("INFO", v...)
	} else {
		r.logger.V(0).Info(fmt.Sprintf("[INFO] %s", fmt.Sprintf(format, v...)))
	}
}

func (r *raftLogr) Warning(v ...any) {
	r.logger.V(0).Info("WARN", v...)
}
func (r *raftLogr) Warningf(format string, v ...any) {
	if format == "" {
		r.logger.V(0).Info("WARN", v...)
	} else {
		r.logger.V(0).Info(fmt.Sprintf("[WARN] %s", fmt.Sprintf(format, v...)))
	}
}

func (r *raftLogr) Fatal(v ...any) {
	r.Error(v...)
	os.Exit(1)
}
func (r *raftLogr) Fatalf(format string, v ...any) {
	r.Errorf(format, v...)
	os.Exit(1)
}

func (r *raftLogr) Panic(v ...any) {
	r.Error(v...)
	panic("unexpected error")
}
func (r *raftLogr) Panicf(format string, v ...any) {
	r.Errorf(format, v...)
	panic(fmt.Sprintf(format, v...))
}

var _ raftv4.Logger = (*raftLogr)(nil)
