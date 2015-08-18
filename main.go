package main

import (
	"flag"
	"fmt"
	"os"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/Sirupsen/logrus"
	"github.com/alouca/gosnmp"
	"golang.org/x/net/context"
)

var snmp SnmpManager

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s MOUNTPOINT\n", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	var err error

	// getopt
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 2 {
		usage()
		os.Exit(2)
	}
	mountpoint := flag.Arg(0)
	snmpServer := flag.Arg(1)

	// connect snmp
	snmp.client, err = gosnmp.NewGoSNMP(snmpServer, "public", gosnmp.Version2c, 5)
	if err != nil {
		logrus.Fatalf("gosnmp.NewGoSNMP: %v", err)
	}
	//snmp.client.SetDebug(true)
	//snmp.client.SetVerbose(true)
	snmp.currentId = 1
	snmp.cache = make(map[string]SnmpCacheEntry)
	snmp.cacheMap = make(map[uint64]string)

	// preload initial cache
	err = snmp.LoadWalk(".1.3.6.1")
	if err != nil {
		logrus.Fatalf("snmp.LoadWalk: %v", err)
	}

	// mount fuse
	c, err := fuse.Mount(
		mountpoint,
		fuse.FSName("fuse-snmp"),
		fuse.Subtype("snmpfs"),
		fuse.LocalVolume(),
		fuse.VolumeName("Fuse SNMP"),
	)
	if err != nil {
		logrus.Fatalf("fuse.Mount: %v", err)
	}
	defer c.Close()

	// map fuse
	err = fs.Serve(c, FS{})
	if err != nil {
		logrus.Fatalf("fs.Serve: %v", err)
	}

	// wait for fuse close
	<-c.Ready
	if err := c.MountError; err != nil {
		logrus.Fatalf("c.MountError: %v", err)
	}

	logrus.Fatalf("BYEBYE")
}

// SnmpManger communicates with a snmp server and keep cache for resolved OIDs
type SnmpManager struct {
	client    *gosnmp.GoSNMP
	currentId uint64
	cache     map[string]SnmpCacheEntry
	cacheMap  map[uint64]string
}

type SnmpCacheEntry struct {
	pdu   gosnmp.SnmpPDU
	inode uint64
}

func (s *SnmpManager) LoadWalk(oid string) error {
	results, err := snmp.client.BulkWalk(8, oid)
	if err != nil {
		logrus.Fatalf("snmp.BulkWalk: %v", err)
	}
	for _, v := range results {
		if entry := s.cache[v.Name]; entry.inode > 0 {
			logrus.Infof("MATCHED: %s", v.Name)
		} else {
			entry := SnmpCacheEntry{
				pdu:   v,
				inode: s.currentId,
			}
			s.currentId++
			s.cache[v.Name] = entry
			s.cacheMap[entry.inode] = v.Name
			logrus.Infof("NOT MATCHED: %s", v.Name)
		}
	}
	return nil
}

// FS implements the snmpfs file system.
type FS struct{}

func (FS) Root() (fs.Node, error) {
	logrus.Infof("Root")
	return Dir{}, nil
}

// Dir implements a snmpfs directory
type Dir struct{}

func (Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	logrus.Infof("Dir.Attr: ctx=%q attr:%q", ctx, a)
	a.Inode = 1
	a.Mode = os.ModeDir | 0555
	return nil
}

func (Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	logrus.Infof("Dir.Lookup: ctx=%q, name=%q", ctx, name)
	if entry := snmp.cache[name]; entry.inode > 0 {
		return &File{Inode: entry.inode}, nil
	}
	return nil, fuse.ENOENT
}

func (Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	logrus.Infof("Dir.ReadDirAll: ctx=%q", ctx)
	dir := []fuse.Dirent{}
	for _, entry := range snmp.cache {
		dir = append(dir, fuse.Dirent{
			Inode: entry.inode,
			Name:  entry.pdu.Name,
			Type:  fuse.DT_File,
		})
	}
	return dir, nil
}

// File implements a snmpfs file
type File struct {
	Inode uint64
}

func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Inode = f.Inode
	// FIXME: compute the size dynamically
	a.Size = uint64(100)
	a.Mode = 0444
	logrus.Infof("File.Attr: ctx=%q, a=%q", ctx, a)
	return nil
}

func (f *File) ReadAll(ctx context.Context) ([]byte, error) {
	logrus.Infof("File.ReadAll: ctx=%q, file=%q", ctx, f)
	oid := snmp.cacheMap[f.Inode]
	entry := snmp.cache[oid].pdu
	switch entry.Type {
	case gosnmp.OctetString:
		return []byte(entry.Value.(string) + "\n"), nil
	case gosnmp.TimeTicks:
		return []byte(fmt.Sprintf("%d", entry.Value) + "\n"), nil
	case gosnmp.ObjectIdentifier:
		// FIXME: print full oid
		// BONUS: create a symbolic link !
		return []byte(fmt.Sprintf("%d", entry.Value) + "\n"), nil
	default:
		logrus.Warnf("Response: %q %q %q", entry.Name, entry.Value, entry.Type.String())
	}
	return []byte(fmt.Sprintf("error unknown type: %s", entry.Type.String())), nil
}
