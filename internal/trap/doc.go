// Package trap implements decoy TCP listeners on ports not occupied by real
// services. Each enabled port runs a listener that answers with the protocol
// emulator selected in the configuration, records a connection event with the
// source address, and never reveals that it is a decoy.
package trap
