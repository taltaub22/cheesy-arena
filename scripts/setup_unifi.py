#!/usr/bin/env python3
"""Sets up the Cheesy Arena field networks on a UniFi gateway (such as a UDM Pro).

Creates the FMS network (VLAN 100, 10.0.100.1/24) and the six alliance station networks (VLANs 10-60) if they are
missing. Station networks that already exist are left alone, because Cheesy Arena changes their subnets for every match.
The FMS network is corrected if its subnet or DHCP range is wrong. Nothing is changed if a network would overlap another
one or if two networks share a VLAN ID.

Uses only the Python standard library. Examples:

    python3 setup_unifi.py --host 192.168.1.1 --username fms-admin --dry-run
    python3 setup_unifi.py --host 192.168.1.1 --username fms-admin
    python3 setup_unifi.py --host 192.168.1.1 --api-key <key>

The password is asked for if it isn't given with --password or the UNIFI_PASSWORD environment variable.
"""

import argparse
import getpass
import http.cookiejar
import ipaddress
import json
import os
import ssl
import sys
import urllib.error
import urllib.request

SITE = "default"

FMS = {
    "name": "FMS",
    "vlan": 100,
    "subnet": "10.0.100.1/24",
    "dhcp": ("10.0.100.170", "10.0.100.254"),
}

# The station networks, in the same order and with the same placeholder subnets as Cheesy Arena (network/switch.go).
STATIONS = [
    {"name": name, "vlan": vlan, "subnet": f"10.255.{vlan}.4/24", "dhcp": None}
    for name, vlan in [("Red 1", 10), ("Red 2", 20), ("Red 3", 30), ("Blue 1", 40), ("Blue 2", 50), ("Blue 3", 60)]
]

MANUAL_STEPS = """
Still to do by hand in UniFi Network:
  1. On each station network (VLAN 10-60): turn off Auto-scale, IPv6, mDNS and content filtering, and leave
     "Isolate Network" off (it would also block the driver stations from reaching the FMS).
  2. Firewall (Settings > Security > Zones): put the six station networks in a custom zone (e.g. "Field"), then:
       - Allow Field -> 10.0.100.5 on TCP 1750 and UDP 1160 (driver station to FMS)
       - Allow Internal -> Field (FMS to driver station on UDP 1121)
       - Block Field -> Field, Field -> External and all other Field -> Internal traffic
  3. Switch ports: uplink to the UDM = Allow all; each station port = that station's network with tagged VLANs
     blocked; FMS computer, displays and tablets = FMS (VLAN 100); a field access point = FMS native with VLANs
     10-60 tagged.
  4. Give the FMS computer the static address 10.0.100.5/24 with gateway 10.0.100.1.
  5. In Cheesy Arena (Settings > Network Switch), set the Switch Address to https://10.0.100.1, the Switch API Key
     to an API key created in UniFi Network (Settings > Control Plane > Integrations), and turn on switch management. Then load a test match and check that each
     station port gets a 10.TE.AM.x address.
"""


class SetupError(Exception):
    pass


class UniFi:
    """A session with the UniFi Network API on a UniFi OS gateway."""

    def __init__(self, host, verify_tls=False):
        self.base_url = (host if "://" in host else "https://" + host).rstrip("/")
        context = ssl.create_default_context()
        if not verify_tls:
            # The gateway uses a self-signed certificate by default.
            context.check_hostname = False
            context.verify_mode = ssl.CERT_NONE
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
            urllib.request.HTTPSHandler(context=context),
        )
        self.headers = {"Accept": "application/json"}

    def use_api_key(self, api_key):
        self.headers["X-API-KEY"] = api_key

    def login(self, username, password):
        try:
            self.request("POST", "/api/auth/login", {"username": username, "password": password, "rememberMe": False})
        except SetupError as error:
            raise SetupError(f"UniFi login failed: {error}") from None

    def request(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        headers = dict(self.headers)
        if data is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base_url + path, data=data, method=method, headers=headers)
        try:
            with self.opener.open(request, timeout=15) as response:
                self.update_csrf_token(response.headers)
                text = response.read().decode()
        except urllib.error.HTTPError as error:
            message = error.read().decode(errors="replace").strip()[:200]
            raise SetupError(f"{method} {path} returned status {error.code}: {message}") from None
        except urllib.error.URLError as error:
            raise SetupError(f"unable to reach {self.base_url}: {error.reason}") from None
        return json.loads(text) if text else {}

    def update_csrf_token(self, headers):
        # The gateway hands out a CSRF token at login and may refresh it later; it's needed for any change.
        token = headers.get("X-Updated-CSRF-Token") or headers.get("X-CSRF-Token")
        if token:
            self.headers["X-CSRF-Token"] = token

    def network_request(self, method, network_id="", body=None):
        path = f"/proxy/network/api/s/{SITE}/rest/networkconf" + (f"/{network_id}" if network_id else "")
        response = self.request(method, path, body)
        meta = response.get("meta", {})
        if meta.get("rc") != "ok":
            raise SetupError(f"UniFi returned an error: {meta.get('msg', response)}")
        return response.get("data", [])

    def list_networks(self):
        return self.network_request("GET")

    def create_network(self, spec):
        body = {
            "name": spec["name"],
            "purpose": "corporate",
            "networkgroup": "LAN",
            "enabled": True,
            "vlan_enabled": True,
            "vlan": spec["vlan"],
            "ip_subnet": spec["subnet"],
            "dhcpd_enabled": spec["dhcp"] is not None,
        }
        if spec["dhcp"]:
            body["dhcpd_start"], body["dhcpd_stop"] = spec["dhcp"]
        try:
            data = self.network_request("POST", body=body)
        except SetupError as error:
            raise SetupError(f"unable to create network {spec['name']!r}: {error}") from None
        return data[0] if data else body

    def update_network(self, network, spec):
        # Send back everything that was received, with only the managed fields changed.
        body = dict(network)
        body["ip_subnet"] = spec["subnet"]
        body["dhcpd_enabled"] = spec["dhcp"] is not None
        if spec["dhcp"]:
            body["dhcpd_start"], body["dhcpd_stop"] = spec["dhcp"]
        try:
            self.network_request("PUT", network["_id"], body)
        except SetupError as error:
            raise SetupError(f"unable to update network {network.get('name')!r}: {error}") from None
        network.update(body)


def network_vlan(network):
    """Returns the network's VLAN ID, which UniFi may report as a number or a string, or None if it has none."""
    try:
        return int(network.get("vlan"))
    except (TypeError, ValueError):
        return None


def overlapping_network(subnet, networks, exclude=None):
    """Returns the first network other than the excluded one whose subnet overlaps the given one, or None."""
    target = ipaddress.ip_interface(subnet).network
    for network in networks:
        if network is exclude or not network.get("ip_subnet"):
            continue
        try:
            current = ipaddress.ip_interface(network["ip_subnet"]).network
        except ValueError:
            continue
        if target.overlaps(current):
            return network
    return None


def matches(network, spec):
    if network.get("ip_subnet") != spec["subnet"]:
        return False
    if bool(network.get("dhcpd_enabled")) != (spec["dhcp"] is not None):
        return False
    return not spec["dhcp"] or (network.get("dhcpd_start"), network.get("dhcpd_stop")) == spec["dhcp"]


def describe(subnet, dhcp_enabled, dhcp_start=None, dhcp_stop=None):
    return f"{subnet}, DHCP " + (f"{dhcp_start}-{dhcp_stop}" if dhcp_enabled else "off")


def plan(networks):
    """Checks the gateway's networks and returns the changes to make, as (action, spec, network) tuples. Raises a
    SetupError before anything is changed if any network can't be set up."""
    changes = []
    planned = list(networks)  # The networks as they will be once the planned creations are done.
    for spec, managed in [(FMS, True)] + [(station, False) for station in STATIONS]:
        label = f"{spec['name']} (VLAN {spec['vlan']})"
        existing = [network for network in networks if network_vlan(network) == spec["vlan"]]
        if len(existing) > 1:
            names = ", ".join(repr(network.get("name")) for network in existing)
            raise SetupError(f"more than one UniFi network has VLAN ID {spec['vlan']} ({names}); remove the extra ones")

        if not existing:
            other = overlapping_network(spec["subnet"], planned)
            if other:
                raise SetupError(
                    f"can't create {label} with {spec['subnet']} because network {other.get('name')!r} already uses "
                    f"{other['ip_subnet']}; change or remove that network first"
                )
            changes.append(("create", spec, None))
            planned.append({"name": spec["name"], "ip_subnet": spec["subnet"]})
            continue

        network = existing[0]
        if not managed:
            changes.append(("keep", spec, network))
        elif matches(network, spec):
            changes.append(("correct", spec, network))
        else:
            other = overlapping_network(spec["subnet"], planned, exclude=network)
            if other:
                raise SetupError(
                    f"can't set {network.get('name')!r} to {spec['subnet']} because network {other.get('name')!r} "
                    f"already uses {other['ip_subnet']}; change or remove that network first"
                )
            changes.append(("update", spec, network))
    return changes


def set_up(unifi, dry_run):
    for action, spec, network in plan(unifi.list_networks()):
        label = f"{spec['name']} (VLAN {spec['vlan']})"
        wanted = describe(spec["subnet"], spec["dhcp"] is not None, *(spec["dhcp"] or ()))
        if action == "create":
            if not dry_run:
                unifi.create_network(spec)
            print(f"  {label}: {'would create' if dry_run else 'created'} with {wanted}")
        elif action == "update":
            current = describe(
                network.get("ip_subnet"), network.get("dhcpd_enabled"), network.get("dhcpd_start"),
                network.get("dhcpd_stop"),
            )
            if not dry_run:
                unifi.update_network(network, spec)
            print(f"  {label}: {'would update' if dry_run else 'updated'} {network.get('name')!r} from {current} to {wanted}")
        elif action == "correct":
            print(f"  {label}: already correct as {network.get('name')!r}")
        else:
            print(f"  {label}: already exists as {network.get('name')!r}; left as is")


def main():
    parser = argparse.ArgumentParser(description="Set up the Cheesy Arena field networks on a UniFi gateway.")
    parser.add_argument("--host", required=True, help="gateway address or URL, e.g. 192.168.1.1")
    parser.add_argument("--username", help="local UniFi OS admin username")
    parser.add_argument("--password", help="admin password (default: UNIFI_PASSWORD, or asked for)")
    parser.add_argument("--api-key", help="UniFi API key, used instead of a username and password")
    parser.add_argument("--dry-run", action="store_true", help="only show what would be changed")
    parser.add_argument("--verify-tls", action="store_true", help="check the gateway's TLS certificate")
    args = parser.parse_args()

    unifi = UniFi(args.host, args.verify_tls)
    try:
        if args.api_key:
            unifi.use_api_key(args.api_key)
        else:
            if not args.username:
                parser.error("give --username (and a password), or --api-key")
            password = args.password or os.environ.get("UNIFI_PASSWORD") or getpass.getpass("UniFi password: ")
            unifi.login(args.username, password)

        print(f"{'Dry run against' if args.dry_run else 'Setting up the field networks on'} {unifi.base_url}:", flush=True)
        set_up(unifi, args.dry_run)
    except SetupError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    print(MANUAL_STEPS)
    return 0


if __name__ == "__main__":
    sys.exit(main())
