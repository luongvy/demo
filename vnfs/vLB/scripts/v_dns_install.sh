#!/bin/bash

NEXUS_ARTIFACT_REPO=$(cat /opt/config/nexus_artifact_repo.txt)
DEMO_ARTIFACTS_VERSION=$(cat /opt/config/demo_artifacts_version.txt)
if [[ "$DEMO_ARTIFACTS_VERSION" =~ "SNAPSHOT" ]]; then REPO=snapshots; else REPO=releases; fi
INSTALL_SCRIPT_VERSION=$(cat /opt/config/install_script_version.txt)
CLOUD_ENV=$(cat /opt/config/cloud_env.txt)

# Convert Network CIDR to Netmask
cdr2mask () {
	# Number of args to shift, 255..255, first non-255 byte, zeroes
	set -- $(( 5 - ($1 / 8) )) 255 255 255 255 $(( (255 << (8 - ($1 % 8))) & 255 )) 0 0 0
	[ $1 -gt 1 ] && shift $1 || shift
	echo ${1-0}.${2-0}.${3-0}.${4-0}
}

# OpenStack network configuration
if [[ $CLOUD_ENV == "openstack" ]]
then
	echo 127.0.0.1 $(hostname) >> /etc/hosts

	# Allow remote login as root
	mv /root/.ssh/authorized_keys /root/.ssh/authorized_keys.bk
	cp /home/ubuntu/.ssh/authorized_keys /root/.ssh

	MTU=$(/sbin/ifconfig | grep MTU | sed 's/.*MTU://' | sed 's/ .*//' | sort -n | head -1)

	IP=$(cat /opt/config/local_private_ipaddr.txt)
	BITS=$(cat /opt/config/vlb_private_net_cidr.txt | cut -d"/" -f2)
	NETMASK=$(cdr2mask $BITS)
	echo "auto eth1" >> /etc/network/interfaces
	echo "iface eth1 inet static" >> /etc/network/interfaces
	echo "    address $IP" >> /etc/network/interfaces
	echo "    netmask $NETMASK" >> /etc/network/interfaces
	echo "    mtu $MTU" >> /etc/network/interfaces

	IP=$(cat /opt/config/oam_private_ipaddr.txt)
	BITS=$(cat /opt/config/onap_private_net_cidr.txt | cut -d"/" -f2)
	NETMASK=$(cdr2mask $BITS)
	echo "auto eth2" >> /etc/network/interfaces
	echo "iface eth2 inet static" >> /etc/network/interfaces
	echo "    address $IP" >> /etc/network/interfaces
	echo "    netmask $NETMASK" >> /etc/network/interfaces
	echo "    mtu $MTU" >> /etc/network/interfaces
fi

# Download required dependencies
echo "deb http://ppa.launchpad.net/openjdk-r/ppa/ubuntu $(lsb_release -c -s) main" >>  /etc/apt/sources.list.d/java.list
echo "deb-src http://ppa.launchpad.net/openjdk-r/ppa/ubuntu $(lsb_release -c -s) main" >>  /etc/apt/sources.list.d/java.list
apt-get update
apt-get install --allow-unauthenticated -y wget openjdk-8-jdk bind9 bind9utils bind9-doc apt-transport-https ca-certificates
sleep 1

# Download vDNS demo code for DNS Server
mkdir /opt/FDclient
cd /opt

unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip v_dns_init.sh > /opt/v_dns_init.sh
unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip vdns.sh > /opt/vdns.sh
unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip dnsclient.sh > /opt/dnsclient.sh
unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip set_gre_tunnel.sh > /opt/set_gre_tunnel.sh
wget -O dns-client-$DEMO_ARTIFACTS_VERSION.jar "${NEXUS_ARTIFACT_REPO}/service/local/artifact/maven/redirect?r=${REPO}&g=org.onap.demo.vnf.vlb&a=dns-client&e=jar&v=$DEMO_ARTIFACTS_VERSION"

mv dns-client-$DEMO_ARTIFACTS_VERSION.jar /opt/FDclient/
mv dnsclient.sh /opt/FDclient/
mv set_gre_tunnel.sh /opt/FDclient/

chmod +x v_dns_init.sh
chmod +x vdns.sh
chmod +x /opt/FDclient/dnsclient.sh
chmod +x /opt/FDclient/set_gre_tunnel.sh

# Download Bind config files
cd /opt/config
unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip db_dnsdemo_onap_org > /opt/db_dnsdemo_onap_org
unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip named.conf.options > /opt/named.conf.options
unzip -p -j /opt/vlb-scripts-$INSTALL_SCRIPT_VERSION.zip named.conf.local > /opt/named.conf.local

# Configure Bind
modprobe ip_gre
mkdir /etc/bind/zones
sed -i "s/OPTIONS=.*/OPTIONS=\"-4 -u bind\"/g" /etc/default/bind9
mv db_dnsdemo_onap_org /etc/bind/zones/db.dnsdemo.onap.org
mv named.conf.options /etc/bind/
mv named.conf.local /etc/bind/
sleep 1

# Run instantiation script
cd /opt
mv vdns.sh /etc/init.d
update-rc.d vdns.sh defaults

# Rename network interface in openstack Ubuntu 16.04 images. Then, reboot the VM to pick up changes
if [[ $CLOUD_ENV != "rackspace" ]]
then
	sed -i "s/GRUB_CMDLINE_LINUX=.*/GRUB_CMDLINE_LINUX=\"net.ifnames=0 biosdevname=0\"/g" /etc/default/grub
	grub-mkconfig -o /boot/grub/grub.cfg
	sed -i "s/ens[0-9]*/eth0/g" /etc/network/interfaces.d/*.cfg
	sed -i "s/ens[0-9]*/eth0/g" /etc/udev/rules.d/70-persistent-net.rules
	echo 'network: {config: disabled}' >> /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg
	echo "APT::Periodic::Unattended-Upgrade \"0\";" >> /etc/apt/apt.conf.d/10periodic
	reboot
fi

./v_dns_init.sh