package main

import (
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"log"
	"os"
	"time"
)

type cmdLineOpts struct {
	filters     string
	blockDevice string
	createFs    bool
	fsType      string
	mountFs     bool
	mountPoint  string
	help        bool
	version     bool
}

var (
	opts              cmdLineOpts
	region            string
	ec2c              *ec2.EC2
	filters           []*ec2.Filter
	volumeAttachTries int
)

func init() {
	flag.StringVar(&opts.filters, "filters", "", "a comma-delimited list of filters. For example --filters='tag-key=Env,Profile=foo'")
	flag.StringVar(&opts.blockDevice, "block-device", "/dev/xvde", "linux block device path")
	flag.BoolVar(&opts.createFs, "create-file-system", false, "whether to create a file system")
	flag.StringVar(&opts.fsType, "file-system-type", "ext4", "file system type")
	flag.BoolVar(&opts.mountFs, "mount-fs", false, "whether to mount a file system")
	flag.StringVar(&opts.mountPoint, "mount-point", "/data", "mount point path")
	flag.BoolVar(&opts.help, "help", false, "print this message")
	flag.BoolVar(&opts.version, "version", false, "print version and exit")
}

func main() {
	flag.Parse()

	if flag.NArg() > 0 || opts.help {
		fmt.Fprintf(os.Stderr, "Usage: %q [OPTION]...\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}

	if opts.version {
		fmt.Fprintln(os.Stderr, Version)
		os.Exit(0)
	}

	var i instance
	err := i.getMetadata()
	if err != nil {
		log.Fatalf("Issues getting instance metadata properties. Exiting..")
	}
	ec2c = ec2.New(session.New(), aws.NewConfig().WithRegion(i.region))
	filters = buildFilters(i)

	for {
		run(&i)
		time.Sleep(10 * time.Second)
	}
}

func run(i *instance) {
	// Iterate over found volumes and check if one of them is attached to the
	// instance, then update i.volume accordingly.
	volumes, err := findVolumes(i, ec2c, filters)
	if err != nil {
		log.Println(err)
	} else {
		for _, v := range volumes {
			if i.volume == nil && v.attachedTo == i.id {
				log.Printf("Found attached volume: %q.\n", v.id)
				if opts.createFS {
					if !hasFs(opts.blockDevice, opts.fsType) {
						mkfs(opts.blockDevice, opts.fsType)

					}
				}
				i.volume = &v
				break
			}
			if i.volume != nil && i.volume.id == v.id && v.available {
				i.volume = nil
				break
			}
		}
	}

	// Iterate over found network interfaces and see if one of them is attached
	// to the instance, then update i.networkInterface accordingly.
	networkInterfaces, err := findNetworkInterfaces(i, ec2c, filters)
	if err != nil {
		log.Println(err)
	} else {
		for _, n := range networkInterfaces {
			if i.networkInterface == nil && n.attachedTo == i.id {
				log.Printf("Found attached network interface: %q.\n", n.id)
				i.networkInterface = &n
				break
			}
			if i.networkInterface != nil && i.networkInterface.id == n.id && n.available {
				i.networkInterface = nil
				break
			}
		}
	}

	// If nothing is attached, then pick an available volume. We never want to
	// attach a network interface if there is no volume attached first.
	if i.volume == nil && i.networkInterface == nil {
		log.Println("Neither a volume, nor a network interface are attached.")
		for _, v := range volumes {
			if v.available {
				if err := i.attachVolume(v, ec2c); err == nil {
					if opts.createFs && !hasFs(opts.blockDevice, opts.fsType) {
						mkfs(opts.blockDevice, opts.fsType)
					}
				}
				break
			}
		}
		if i.volume == nil {
			log.Println("No available volumes found.")
		}
		if i.volume != nil {
			for _, n := range networkInterfaces {
				if n.available && i.volume.nodeID == n.nodeID {
					_ = i.attachNetworkInterface(n, ec2c)
					break
				}
				log.Println("No available network interfaces found.")
			}
		} else {
			log.Println("No volumes appear to be attached, skipping network interface attachment.")
		}
	}

	// If volume is attached, but network interface is not, then find a
	// matching available network interface and attach it.
	if i.volume != nil && i.networkInterface == nil {
		for _, n := range networkInterfaces {
			if n.available && n.nodeID == i.volume.nodeID {
				_ = i.attachNetworkInterface(n, ec2c)
				break
			}
		}
	}

	// If network interface is attached, but volume is not, then find a
	// matching available volume and attach it.
	if i.networkInterface != nil && i.volume == nil {
		for _, v := range volumes {
			if v.available && v.nodeID == i.networkInterface.nodeID {
				log.Printf("Found a matching volume %q with NodeID %q.\n", v.id, v.nodeID)
				_ = i.attachVolume(v, ec2c)
				if opts.createFS {
					if !hasFs(opts.blockDevice, opts.fsType) {
						mkfs(opts.blockDevice, opts.fsType)

					}
				}
				break
			}
		}
	}

	// Set nodeID only when both volume and network interface are attached and their node IDs match.
	if i.volume != nil && i.networkInterface != nil {
		if i.volume.nodeID == i.networkInterface.nodeID {
			if i.nodeID != i.volume.nodeID {
				i.nodeID = i.volume.nodeID
				log.Printf("Node ID was set to: %q.\n", i.nodeID)
			}
		}
		if i.volume.nodeID != i.networkInterface.nodeID {
			log.Printf("Something has gone wrong, volume and network interface node IDs do not match.")
		}
	}
}
