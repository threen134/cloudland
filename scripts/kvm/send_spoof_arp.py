#!/usr/bin/env python3

# Sends one gratuitous ARP reply for an address of a guest, from the guest's MAC, broadcast on a bridge: the bridge
# and the switches learn where that MAC is, and the hosts on the segment update their ARP caches.
# send_spoof_arp.py <interface> <ip> <mac>
# Standard library only: the earlier version needed scapy, which the deploy scripts never installed, so it failed on
# every node (R2-17, 2026-10-17). Errors go to stderr; the exit status is 0 either way, as the callers do not wait.
import socket
import struct
import sys


def gratuitous_arp(ip, mac):
    hw = bytes.fromhex(mac.replace(':', ''))
    if len(hw) != 6:
        raise ValueError(f"bad MAC address {mac}")
    addr = socket.inet_aton(ip)
    broadcast = b'\xff' * 6
    ethernet = broadcast + hw + struct.pack('!H', 0x0806)
    # hardware Ethernet, protocol IPv4, address lengths 6 and 4, operation 2 (reply); sender and target are the same IP
    arp = struct.pack('!HHBBH', 1, 0x0800, 6, 4, 2) + hw + addr + broadcast + addr
    return ethernet + arp


def main():
    if len(sys.argv) != 4:
        print("Usage: send_spoof_arp.py <interface> <source_ip> <source_mac>", file=sys.stderr)
        sys.exit(1)
    iface, ip, mac = sys.argv[1:]
    try:
        with socket.socket(socket.AF_PACKET, socket.SOCK_RAW) as s:
            s.bind((iface, 0))
            s.send(gratuitous_arp(ip, mac))
    except (OSError, ValueError) as e:
        print(f"Failed to send a gratuitous ARP for {ip} ({mac}) on {iface}: {e}", file=sys.stderr)


if __name__ == "__main__":
    main()
