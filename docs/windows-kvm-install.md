# Installing Windows 11 on QEMU/KVM (virt-manager)

## Prerequisites

```bash
sudo pacman -S qemu-full libvirt virt-manager edk2-ovmf swtpm virt-firmware
sudo systemctl enable --now libvirtd
```

- `edk2-ovmf` — UEFI firmware (OVMF) for VMs
- `swtpm` — software TPM 2.0 emulator (required by Windows 11)
- `virt-firmware` — tool to enroll Secure Boot keys into NVRAM

## Step 1: Prepare UEFI NVRAM with Microsoft Secure Boot Keys

Arch's `edk2-ovmf` ships OVMF_VARS without Microsoft keys enrolled. Windows 11 requires Secure Boot, so you must enroll them manually:

```bash
virt-fw-vars \
  --input /usr/share/edk2/x64/OVMF_VARS.4m.fd \
  --output /tmp/win11_VARS_secboot.fd \
  --secure-boot \
  --enroll-redhat
```

This enrolls Microsoft Windows + UEFI CA certificates (2011 & 2023) plus Red Hat keys into the NVRAM.

Copy to libvirt's NVRAM location after VM creation:

```bash
sudo cp /tmp/win11_VARS_secboot.fd /var/lib/libvirt/qemu/nvram/<vm_name>_VARS.fd
sudo chown libvirt-qemu:libvirt-qemu /var/lib/libvirt/qemu/nvram/<vm_name>_VARS.fd
```

## Step 2: Create the VM

```bash
virt-install --connect qemu:///system \
  --name win11 \
  --ram 8192 \
  --vcpus 4 \
  --cpu host-passthrough \
  --os-variant win11 \
  --machine q35 \
  --boot uefi,cdrom,hd \
  --disk path=/var/lib/libvirt/images/win11.qcow2,size=35,bus=sata \
  --cdrom /path/to/Win11_25H2_EnglishInternational_x64.iso \
  --tpm backend.type=emulator,backend.version=2.0,model=tpm-crb \
  --network network=default,model=e1000e \
  --graphics spice \
  --video qxl \
  --noautoconsole
```

Key points:
- **`--cpu host-passthrough`** — exposes host CPU features to the VM
- **`--machine q35`** — modern chipset with PCIe support
- **`--boot uefi`** — uses OVMF UEFI firmware
- **`--tpm`** — adds TPM 2.0 (Windows 11 requirement)
- **`--disk bus=sata`** — use SATA so Windows sees the disk without extra drivers. Alternatively use `bus=virtio` for better performance, but you'll need to load virtio-win drivers during installation.

## Step 3: Enable Secure Boot in VM Config

After creation, virt-install may set `enrolled-keys=no`. Verify and fix:

```bash
virsh -c qemu:///system dumpxml win11 | grep -E "secure-boot|enrolled-keys|loader"
```

Ensure:
- `secure-boot` = `yes`
- loader = `OVMF_CODE.secboot.4m.fd`
- `secure='yes'` on the loader element

If not, edit:

```bash
EDITOR="sed -i \"\
  s|<feature enabled='no' name='secure-boot'/>|<feature enabled='yes' name='secure-boot'/>|;\
  s|OVMF_CODE.4m.fd|OVMF_CODE.secboot.4m.fd|;\
  s|secure='no'|secure='yes'|\"" \
virsh -c qemu:///system edit win11
```

Then copy the prepared NVRAM with enrolled keys (Step 1) to the path shown in the VM's `<nvram>` element.

## Step 4: Boot and Press Any Key

This is critical: when the VM starts, the UEFI shows:

```
Press any key to boot from CD or DVD......
```

**You have ~3 seconds to press a key.** If you miss it, UEFI falls through to the empty disk and shows "No bootable option or device was found."

To send keypresses automatically via CLI:

```bash
virsh -c qemu:///system start win11
for i in $(seq 1 15); do
  virsh -c qemu:///system qemu-monitor-command win11 --hmp "sendkey ret"
  sleep 1
done
```

## Step 5: Install Windows

The installer should now pass all checks (UEFI, Secure Boot, TPM 2.0) and show the disk selection screen.

If you used `bus=sata`, the 35GB disk appears automatically.

If you used `bus=virtio`, the disk won't appear — click **Load Driver** and load drivers from the [virtio-win ISO](https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/stable-virtio/virtio-win.iso) (attach as second CDROM).

## Passing USB Devices to the VM

To pass a USB device (e.g. YubiKey) to the VM:

1. Find the device vendor/product ID:

```bash
lsusb | grep -i yubi
# Bus 007 Device 002: ID 1050:0407 Yubico.com Yubikey 4/5 OTP+U2F+CCID
```

2. Attach it to the running VM (hot-plug):

```bash
virsh -c qemu:///system attach-device win11 --live <(cat <<EOF
<hostdev mode='subsystem' type='usb'>
  <source>
    <vendor id='0x1050'/>
    <product id='0x0407'/>
  </source>
</hostdev>
EOF
)
```

Replace vendor/product IDs with your device's values from `lsusb`.

You can also do this in virt-manager: click the **lightbulb icon** → **Add Hardware** → **USB Host Device** → select your device → **Finish**.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| "No bootable device" | Missed "Press any key" prompt | Restart VM, press key within 3 seconds |
| "The PC must support Secure Boot" | NVRAM has no Microsoft keys | Run `virt-fw-vars` to enroll keys (Step 1) |
| "The PC must support TPM 2.0" | No TPM device | Add `--tpm` or install `swtpm` |
| No disk in installer | Using virtio bus without drivers | Switch to `bus=sata` or load virtio-win drivers |
| ISO path wrong | File renamed/moved | Check `virsh dumpxml` CDROM source path |

## Summary of Windows 11 VM Requirements

- UEFI firmware (OVMF) — not legacy BIOS
- Secure Boot with Microsoft keys enrolled in NVRAM
- TPM 2.0 (swtpm emulator)
- Q35 machine type
- At least 4GB RAM, 2 vCPUs, 35GB+ disk
