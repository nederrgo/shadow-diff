# Minikube cluster bootstrap for Monarch E2E (virtualbox, kvm2, or none on WSL).
# Source from e2e-reset-minikube.sh; do not execute directly.
#
# ponytail: EXIT trap for docker-env -u lives in the caller script only (once).
# WSL: virtualbox shim, kvm2 (/dev/kvm), or none (host kubelet fallback).
# Intentionally no docker VM driver — same daemon as Kind defeats the eBPF networking goal.

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-minikube}"
MINIKUBE_CONTEXT="${MINIKUBE_CONTEXT:-minikube}"

_minikube() {
  minikube -p "$MINIKUBE_PROFILE" "$@"
}

_is_wsl() {
  grep -qiE 'microsoft|wsl' /proc/version 2>/dev/null
}

_ensure_windows_vbox_shim() {
  command -v VBoxManage >/dev/null 2>&1 && return 0
  local win_vbox="/mnt/c/Program Files/Oracle/VirtualBox/VBoxManage.exe"
  [[ -x "$win_vbox" ]] || return 1
  local shim_dir="${MONARCH_VBOX_SHIM_DIR:-${REPO:-/tmp}/.cache/vbox-shim}"
  mkdir -p "$shim_dir"
  printf '%s\n' '#!/bin/sh' "exec \"$win_vbox\" \"\$@\"" >"$shim_dir/VBoxManage"
  chmod +x "$shim_dir/VBoxManage"
  export PATH="$shim_dir:$PATH"
}

_vbox_available() {
  _ensure_windows_vbox_shim || true
  command -v VBoxManage >/dev/null 2>&1 && VBoxManage --version >/dev/null 2>&1
}

_ensure_kvm_device() {
  if [[ ! -e /dev/kvm ]] && command -v modprobe >/dev/null 2>&1; then
    # ponytail: WSL nested virt needs explicit modprobe after restart; .wslconfig alone is not enough
    modprobe kvm 2>/dev/null || true
    if grep -qi intel /proc/cpuinfo 2>/dev/null; then
      modprobe kvm_intel 2>/dev/null || true
    elif grep -qi amd /proc/cpuinfo 2>/dev/null; then
      modprobe kvm_amd 2>/dev/null || true
    fi
  fi
  [[ -e /dev/kvm ]] || return 1
  # ponytail: modprobe creates root:root 0600; libvirt qemu (libvirt-qemu) needs group kvm
  if [[ "$(id -u)" -eq 0 ]]; then
    local mode group
    mode="$(stat -c '%a' /dev/kvm 2>/dev/null || echo "")"
    group="$(stat -c '%G' /dev/kvm 2>/dev/null || echo "")"
    if [[ "$mode" == "600" && "$group" != "kvm" ]]; then
      chown root:kvm /dev/kvm 2>/dev/null || true
      chmod 660 /dev/kvm 2>/dev/null || true
    fi
    if getent passwd libvirt-qemu >/dev/null 2>&1; then
      usermod -aG kvm libvirt-qemu 2>/dev/null || true
    fi
  fi
  virsh domcapabilities --virttype kvm >/dev/null 2>&1
}

_kvm2_available() {
  _ensure_kvm_device
}

_none_ready() {
  [[ "$(uname -s)" == Linux ]] || return 1
  [[ "$(id -u)" -eq 0 ]] || return 1
  command -v docker >/dev/null 2>&1 || return 1
  if command -v timeout >/dev/null 2>&1; then
    timeout 5 docker info >/dev/null 2>&1 || return 1
  else
    docker info >/dev/null 2>&1 || return 1
  fi
  [[ ! -f /.dockerenv ]]
}

_ensure_crictl() {
  command -v crictl >/dev/null 2>&1 && return 0
  if [[ "$(id -u)" -eq 0 ]] && command -v apt-get >/dev/null 2>&1 \
    && apt-cache show cri-tools >/dev/null 2>&1; then
    echo "==> Install cri-tools (minikube none needs crictl)"
    apt-get update -qq && apt-get install -y -qq cri-tools
    command -v crictl >/dev/null 2>&1 && return 0
  fi

  local ver arch tarball install_dir url
  ver="${MONARCH_CRICTL_VERSION:-v1.35.0}"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *)
      echo "ERROR: unsupported arch for crictl: $(uname -m)" >&2
      exit 1
      ;;
  esac
  install_dir="${MONARCH_CRICTL_DIR:-/usr/local/bin}"
  tarball="crictl-${ver}-linux-${arch}.tar.gz"
  url="https://github.com/kubernetes-sigs/cri-tools/releases/download/${ver}/${tarball}"
  echo "==> Install crictl ${ver} from GitHub (cri-tools not in apt on Ubuntu 20.04)"
  command -v curl >/dev/null 2>&1 || {
    echo "ERROR: curl required to install crictl: sudo apt install curl" >&2
    exit 1
  }
  mkdir -p "$install_dir"
  curl -fsSL "$url" | tar -xz -C "$install_dir" crictl
  chmod +x "${install_dir}/crictl"
  export PATH="${install_dir}:${PATH}"
  command -v crictl >/dev/null 2>&1 || {
    echo "ERROR: failed to install crictl to ${install_dir}" >&2
    exit 1
  }
}

_ensure_apt_bin() {
  local bin="$1" pkg="${2:-$1}"
  command -v "$bin" >/dev/null 2>&1 && return 0
  if [[ "$(id -u)" -eq 0 ]] && command -v apt-get >/dev/null 2>&1; then
    echo "==> Install ${pkg} (minikube none needs ${bin})"
    apt-get update -qq && apt-get install -y -qq "$pkg"
  fi
  command -v "$bin" >/dev/null 2>&1 || {
    echo "ERROR: ${bin} required for minikube none: sudo apt install ${pkg}" >&2
    exit 1
  }
}

_ensure_none_preflight() {
  if ! _none_ready; then
    echo "ERROR: minikube --driver=none preflight failed" >&2
    [[ "$(id -u)" -eq 0 ]] || echo "       Run as root: sudo MINIKUBE_DRIVER=none $0" >&2
    command -v docker >/dev/null 2>&1 || echo "       Install docker" >&2
    docker info >/dev/null 2>&1 || echo "       Start docker daemon" >&2
    [[ -f /.dockerenv ]] && echo "       none driver cannot run inside a container" >&2
    exit 1
  fi
  _ensure_apt_bin conntrack conntrack
  _ensure_crictl
  _ensure_apt_bin socat socat
  _ensure_apt_bin containerd containerd
  _ensure_containerd_config
  _ensure_containerd_sandbox_image
  _ensure_containerd_cgroup_driver
  _ensure_cni_plugins
  _ensure_wsl_systemctl_shim
  _ensure_containerd_running
  _ensure_crictl_containerd
  _ensure_swap_off_none
  _wsl_fixup_docker_host_mount
  _ensure_calico_mount_propagation
  if sysctl -n fs.protected_regular >/dev/null 2>&1; then
    local pr
    pr=$(sysctl -n fs.protected_regular 2>/dev/null || echo 1)
    if [[ "$pr" != "0" ]]; then
      echo "==> Set fs.protected_regular=0 (minikube none on Ubuntu/WSL)"
      sysctl -w fs.protected_regular=0 >/dev/null 2>&1 || \
        echo "WARN: could not set fs.protected_regular=0 — minikube start may fail" >&2
    fi
  fi
}

_cni_plugins_ready() {
  [[ -x /opt/cni/bin/bridge ]] || [[ -x "${MONARCH_CNI_BIN_DIR:-/opt/cni/bin}/bridge" ]]
}

_ensure_cni_plugins() {
  _cni_plugins_ready && return 0
  local ver arch tarball install_dir url
  ver="${MONARCH_CNI_PLUGINS_VERSION:-v1.6.2}"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *)
      echo "ERROR: unsupported arch for CNI plugins: $(uname -m)" >&2
      exit 1
      ;;
  esac
  install_dir="${MONARCH_CNI_BIN_DIR:-/opt/cni/bin}"
  tarball="cni-plugins-linux-${arch}-${ver}.tgz"
  url="https://github.com/containernetworking/plugins/releases/download/${ver}/${tarball}"
  echo "==> Install containernetworking-plugins ${ver} -> ${install_dir}"
  command -v curl >/dev/null 2>&1 || {
    echo "ERROR: curl required to install CNI plugins: sudo apt install curl" >&2
    exit 1
  }
  mkdir -p "$install_dir"
  curl -fsSL "$url" | tar -xz -C "$install_dir"
  _cni_plugins_ready || {
    echo "ERROR: CNI plugins missing under ${install_dir} after install" >&2
    exit 1
  }
}

_containerd_ready() {
  [[ -S /run/containerd/containerd.sock || -S /var/run/containerd/containerd.sock ]]
}

_ensure_containerd_config() {
  mkdir -p /etc/containerd
  [[ -f /etc/containerd/config.toml ]] && return 0
  echo "==> Generate /etc/containerd/config.toml (minikube edits sandbox_image here)"
  containerd config default >/etc/containerd/config.toml
}

_ensure_containerd_sandbox_image() {
  local img="${MONARCH_PAUSE_IMAGE:-registry.k8s.io/pause:3.10.1}"
  _ensure_containerd_config
  if grep -qE 'sandbox_image\s*=' /etc/containerd/config.toml; then
    sed -i -r "s|^( *)sandbox_image = .*$|\1sandbox_image = \"${img}\"|" /etc/containerd/config.toml
  fi
}

_ensure_containerd_cgroup_driver() {
  _ensure_containerd_config
  local want=true
  _has_systemd || want=false
  grep -qE "SystemdCgroup = ${want}" /etc/containerd/config.toml 2>/dev/null && return 0
  echo "==> Set containerd SystemdCgroup=${want} (match kubelet cgroup driver)"
  sed -i -r "s|^( *)SystemdCgroup = .*$|\1SystemdCgroup = ${want}|" /etc/containerd/config.toml
  if pgrep -x containerd >/dev/null 2>&1; then
    pkill -x containerd 2>/dev/null || true
    sleep 1
  fi
}

_ensure_swap_off_none() {
  swapon --show 2>/dev/null | grep -q . || return 0
  echo "==> Disable swap (kubelet on minikube none)"
  swapoff -a 2>/dev/null || echo "WARN: swapoff failed — minikube start passes kubelet.fail-swap-on=false" >&2
}

_wsl_docker_host_mount_broken() {
  _is_wsl || return 1
  local line nf
  line=$(grep '/Docker/host' /proc/mounts 2>/dev/null | head -1) || return 1
  nf=$(awk '{print NF}' <<<"$line")
  [[ "$nf" -gt 6 ]]
}

_wsl_fixup_docker_host_mount() {
  _wsl_docker_host_mount_broken || return 0
  echo "==> Unmount /Docker/host (Docker Desktop /proc/mounts entry breaks kubelet)"
  umount /Docker/host 2>/dev/null || {
    echo "ERROR: umount /Docker/host failed — kubelet cannot start on WSL with this mount" >&2
    echo "       Disable Docker Desktop WSL integration for this distro, or reinstall Docker Desktop to a path without spaces" >&2
    exit 1
  }
}

_ensure_calico_mount_propagation() {
  mkdir -p /var/run/calico /var/lib/calico
  local prop
  prop=$(findmnt -o PROPAGATION -n / 2>/dev/null || true)
  [[ "$prop" == *shared* ]] && return 0
  echo "==> Remount / as rshared (Calico hostPath mount propagation on none driver)"
  mount --make-rshared / 2>/dev/null || {
    echo "ERROR: mount --make-rshared / failed — calico-node needs shared propagation" >&2
    exit 1
  }
}

_wait_calico_ready() {
  echo "==> Wait for Calico node (CNI)"
  if kubectl wait -n kube-system --for=condition=ready pod -l k8s-app=calico-node --timeout=180s >/dev/null 2>&1; then
    return 0
  fi
  echo "ERROR: calico-node not ready — check: kubectl describe pod -n kube-system -l k8s-app=calico-node" >&2
  kubectl get pods -n kube-system -l k8s-app=calico-node -o wide 2>&1 | sed 's/^/       /' >&2
  exit 1
}

_ensure_wsl_systemctl_shim() {
  _is_wsl || return 0
  _has_systemd && return 0
  [[ "$(id -u)" -eq 0 ]] || return 0
  local dest="/usr/local/sbin/systemctl"
  grep -q 'MONARCH_SYSTEMCTL_SHIM=v5' "$dest" 2>/dev/null && return 0
  echo "==> Install WSL systemctl shim (/usr/local/sbin/systemctl; minikube none without systemd)"
  cat >"$dest" <<'EOF'
#!/bin/sh
# MONARCH_SYSTEMCTL_SHIM=v5 — WSL without systemd; minikube none starts containerd + kubelet
_monarch_fixup_wsl_mounts() {
  line=$(grep '/Docker/host' /proc/mounts 2>/dev/null | head -1) || return 0
  nf=$(printf '%s\n' "$line" | wc -w)
  [ "$nf" -le 6 ] && return 0
  umount /Docker/host 2>/dev/null || true
}
_monarch_containerd_cgroup_driver() {
  [ "$(ps -p 1 -o comm= 2>/dev/null)" = "systemd" ] && return 0
  cfg=/etc/containerd/config.toml
  [ -f "$cfg" ] || return 0
  grep -q 'SystemdCgroup = false' "$cfg" && return 0
  sed -i 's/SystemdCgroup = true/SystemdCgroup = false/' "$cfg"
}
_monarch_kubelet_exec_start() {
  for f in /etc/systemd/system/kubelet.service.d/*.conf \
           /usr/lib/systemd/system/kubelet.service.d/*.conf; do
    [ -f "$f" ] || continue
    line=$(sed -n 's/^ExecStart=//p' "$f" | sed '/^$/d' | tail -1)
    [ -n "$line" ] && printf '%s\n' "$line" && return 0
  done
  for unit in /etc/systemd/system/kubelet.service /lib/systemd/system/kubelet.service; do
    [ -f "$unit" ] || continue
    line=$(sed -n 's/^ExecStart=//p' "$unit" | sed '/^$/d' | head -1)
    [ -n "$line" ] && printf '%s\n' "$line" && return 0
  done
  return 1
}
_monarch_kubelet_cgroup_driver() {
  [ "$(ps -p 1 -o comm= 2>/dev/null)" = "systemd" ] && return 0
  for cfg in /var/lib/kubelet/config.yaml /var/lib/kubelet/instance-config.yaml; do
    [ -f "$cfg" ] || continue
    sed -i 's/^cgroupDriver: systemd$/cgroupDriver: cgroupfs/' "$cfg"
    grep -q '^cgroupDriver:' "$cfg" || echo 'cgroupDriver: cgroupfs' >>"$cfg"
  done
}
_monarch_service_active() {
  pgrep -x "$1" >/dev/null 2>&1
}
case "$1" in
  daemon-reload|enable|disable) exit 0 ;;
  is-enabled)
    case "$2" in
      kubelet|containerd) echo enabled; exit 0 ;;
    esac
    ;;
  is-active|status)
    case "$2" in
      kubelet|containerd)
        if _monarch_service_active "$2"; then
          echo active
          exit 0
        fi
        echo inactive
        exit 3
        ;;
    esac
    ;;
  restart|start|stop)
    case "$2" in
      containerd)
        pkill -x containerd 2>/dev/null || true
        [ "$1" = stop ] && exit 0
        sleep 1
        _monarch_containerd_cgroup_driver
        containerd -c /etc/containerd/config.toml >/dev/null 2>&1 &
        exit 0
        ;;
      kubelet)
        pkill -x kubelet 2>/dev/null || true
        [ "$1" = stop ] && exit 0
        sleep 1
        _monarch_fixup_wsl_mounts
        _monarch_kubelet_cgroup_driver
        exec_start=$(_monarch_kubelet_exec_start) || exit 1
        # ponytail: nohup+log; upgrade path is WSL [boot] systemd=true
        nohup sh -c "$exec_start" >>/var/log/kubelet.log 2>&1 &
        exit 0
        ;;
    esac
    ;;
esac
[ -x /usr/bin/systemctl ] && exec /usr/bin/systemctl "$@"
exit 0
EOF
  chmod +x "$dest"
}

_ensure_containerd_running() {
  local fresh_config=0
  if [[ ! -f /etc/containerd/config.toml ]]; then
    _ensure_containerd_config
    fresh_config=1
  fi
  if [[ "$fresh_config" -eq 1 ]] && pgrep -x containerd >/dev/null 2>&1; then
    pkill -x containerd 2>/dev/null || true
    sleep 1
  fi
  if _containerd_ready && [[ "$fresh_config" -eq 0 ]]; then
    return 0
  fi
  if _has_systemd; then
    echo "==> Start containerd (systemd)"
    systemctl restart containerd 2>/dev/null || service containerd restart 2>/dev/null || true
  else
    echo "==> Start containerd (no systemd — WSL)"
    if ! pgrep -x containerd >/dev/null 2>&1; then
      containerd -c /etc/containerd/config.toml >/dev/null 2>&1 &
      sleep 2
    fi
  fi
  local i
  for i in $(seq 1 15); do
    _containerd_ready && return 0
    sleep 1
  done
  echo "ERROR: containerd socket not found (minikube none + k8s 1.24+ needs containerd or cri-dockerd)" >&2
  exit 1
}

_ensure_crictl_containerd() {
  local sock="/run/containerd/containerd.sock"
  [[ -S /var/run/containerd/containerd.sock ]] && sock="/var/run/containerd/containerd.sock"
  if [[ ! -f /etc/crictl.yaml ]] || ! grep -qF 'containerd.sock' /etc/crictl.yaml 2>/dev/null; then
    echo "==> Configure crictl for containerd"
    cat >/etc/crictl.yaml <<EOF
runtime-endpoint: unix://${sock}
image-endpoint: unix://${sock}
EOF
  fi
}

_in_libvirt_group() {
  id -nG "${1:-$(id -un)}" | grep -qw libvirt
}

_has_systemd() {
  [[ "$(ps -p 1 -o comm= 2>/dev/null)" == "systemd" ]]
}

_libvirt_ready() {
  [[ -S /var/run/libvirt/libvirt-sock ]] && virsh uri >/dev/null 2>&1
}

_kvm2_libvirt_stack_ready() {
  [[ -S /run/libvirt/virtlogd-sock || -S /var/run/libvirt/virtlogd-sock ]] &&
    [[ -S /run/libvirt/virtlockd-sock || -S /var/run/libvirt/virtlockd-sock ]] &&
    _libvirt_ready
}

_start_libvirt_daemon() {
  local bin="$1" sock="$2"
  [[ -S "$sock" ]] && return 0
  command -v "$bin" >/dev/null 2>&1 || {
    echo "ERROR: ${bin} not found (apt install libvirt-daemon-system)" >&2
    exit 1
  }
  echo "==> Start ${bin}"
  "$bin" -d 2>/dev/null || true
  local i
  for i in $(seq 1 10); do
    [[ -S "$sock" ]] && return 0
    sleep 1
  done
  echo "ERROR: ${bin} did not create ${sock}" >&2
  exit 1
}

_ensure_libvirtd_running() {
  _ensure_kvm_device || {
    echo "ERROR: KVM unavailable for libvirt (see kvm2 hints from minikube start)" >&2
    exit 1
  }
  _kvm2_libvirt_stack_ready && return 0

  if _has_systemd; then
    echo "==> Start libvirt stack (systemd)"
    systemctl start virtlogd virtlockd libvirtd 2>/dev/null || service libvirtd start 2>/dev/null || true
  else
    echo "==> Start libvirt stack (no systemd — WSL: virtlogd, virtlockd, libvirtd)"
    mkdir -p /run/libvirt /var/run/libvirt
    _start_libvirt_daemon virtlogd /run/libvirt/virtlogd-sock
    _start_libvirt_daemon virtlockd /run/libvirt/virtlockd-sock
    if ! _libvirt_ready; then
      _start_libvirt_daemon libvirtd /run/libvirt/libvirt-sock
    fi
  fi

  local i
  for i in $(seq 1 15); do
    _kvm2_libvirt_stack_ready && return 0
    sleep 1
  done

  echo "ERROR: libvirt stack not ready (need virtlogd-sock, virtlockd-sock, libvirt-sock)" >&2
  if _is_wsl && ! _has_systemd; then
    echo "       WSL: sudo virtlogd -d && sudo virtlockd -d && sudo libvirtd -d" >&2
    echo "       Or enable systemd in /etc/wsl.conf: [boot] systemd=true  (wsl --shutdown)" >&2
  fi
  exit 1
}

_ensure_kvm2_libvirt_group() {
  local user
  user="$(id -un)"
  _in_libvirt_group "$user" && return 0
  if [[ "$(id -u)" -eq 0 ]]; then
    echo "==> Adding ${user} to libvirt group (minikube kvm2 requires it on Ubuntu)"
    usermod -aG libvirt "$user"
    getent group libvirt | grep -qw "$user" || {
      echo "ERROR: failed to add ${user} to libvirt group" >&2
      exit 1
    }
    return 0
  fi
  echo "ERROR: user '${user}' is not in group libvirt (required for minikube --driver=kvm2)" >&2
  echo "       Fix: sudo usermod -aG libvirt,kvm ${user} && newgrp libvirt" >&2
  exit 1
}

_minikube_start() {
  local driver="$1"
  shift
  local -a extra=()
  local watch_pid=
  # ponytail: minikube rewrites containerd config during start; watcher races until start exits
  if [[ "$driver" == none ]] && ! _has_systemd; then
    export PATH="/usr/local/sbin:${PATH}"
    (
      while true; do
        if grep -qE '^\s*SystemdCgroup = true' /etc/containerd/config.toml 2>/dev/null; then
          sed -i -r 's|^( *)SystemdCgroup = true$|\1SystemdCgroup = false|' /etc/containerd/config.toml
          systemctl restart containerd 2>/dev/null || true
        fi
        sleep 1
      done
    ) &
    watch_pid=$!
  fi
  # ponytail: minikube blocks kvm2 as root; WSL shells are often root
  if [[ "$driver" == kvm2 && "$(id -u)" -eq 0 ]]; then
    extra=(--force)
    echo "WARN: kvm2 as root — using minikube --force (non-root + kvm,libvirt groups is cleaner)"
  fi
  local rc=0
  if [[ "$driver" == kvm2 ]] && ! id -nG | grep -qw libvirt; then
    local cmd
    # ponytail: %q quoting so sg -c survives spaces in paths
    printf -v cmd '%q ' minikube -p "$MINIKUBE_PROFILE" "$@" "${extra[@]}"
    sg libvirt -c "${cmd% }" || rc=$?
  else
    _minikube "$@" "${extra[@]}" || rc=$?
  fi
  [[ -n "$watch_pid" ]] && kill "$watch_pid" 2>/dev/null || true
  return "$rc"
}

resolve_minikube_driver() {
  if [[ -n "${MINIKUBE_DRIVER:-}" ]]; then
    echo "$MINIKUBE_DRIVER"
    return 0
  fi
  if _vbox_available; then
    echo virtualbox
    return 0
  fi
  # ponytail: prefer kvm2 for Pixie eBPF on WSL when nested virt is available
  if _is_wsl && _kvm2_available; then
    echo kvm2
    return 0
  fi
  if _is_wsl && _none_ready; then
    echo none
    return 0
  fi
  if _kvm2_available; then
    echo kvm2
    return 0
  fi
  if _none_ready; then
    echo none
    return 0
  fi
  return 1
}

require_minikube() {
  command -v minikube >/dev/null 2>&1 || {
    echo "ERROR: minikube not found (install: https://minikube.sigs.k8s.io/docs/start/)" >&2
    exit 1
  }
  command -v kubectl >/dev/null 2>&1 || {
    echo "ERROR: kubectl not found" >&2
    exit 1
  }
}

_minikube_driver_unavailable() {
  echo "ERROR: no minikube driver available on this host" >&2
  if _is_wsl; then
    echo "       WSL Ubuntu (no docker VM driver — that would match Kind networking):" >&2
    echo "         1. Install VirtualBox on Windows, then re-run (script shims VBoxManage.exe)" >&2
    echo "         2. Or kvm2 for Pixie eBPF (recommended): nested virt + libvirt packages" >&2
    echo "         3. Or none driver (host kubelet fallback): sudo MINIKUBE_DRIVER=none ..." >&2
    echo "            .wslconfig: [wsl2] nestedVirtualization=true  (restart WSL)" >&2
    echo "            sudo apt install qemu-kvm libvirt-daemon-system libvirt-clients" >&2
  else
    echo "         Install VirtualBox, enable KVM (/dev/kvm), or MINIKUBE_DRIVER=none (root + docker)" >&2
  fi
  echo "         Override: MINIKUBE_DRIVER=virtualbox|kvm2|none" >&2
  exit 1
}

_minikube_start_failed() {
  local driver="$1"
  local err="$2"
  echo "ERROR: minikube start failed (driver=${driver})" >&2
  echo "$err" | tail -25 | sed 's/^/       /' >&2
  if [[ "$driver" == virtualbox ]]; then
    echo "       VirtualBox:" >&2
    echo "         - Install on Windows when using WSL; script adds VBoxManage.exe shim" >&2
    echo "         - BIOS virtualization enabled" >&2
    echo "         - minikube delete -p ${MINIKUBE_PROFILE} && retry if profile corrupted" >&2
  elif [[ "$driver" == kvm2 ]]; then
    echo "       kvm2:" >&2
    if echo "$err" | grep -qi 'GUEST_DRIVER_MISMATCH'; then
      echo "         - existing profile uses a different driver (often none on WSL)" >&2
      echo "         - minikube delete -p ${MINIKUBE_PROFILE}" >&2
      echo "         - MINIKUBE_DRIVER=kvm2 ./testing/scripts/setup-local-pixie.sh" >&2
    fi
    [[ -r /dev/kvm ]] || {
      echo "         - /dev/kvm missing — try: sudo modprobe kvm kvm_intel  (or kvm_amd)" >&2
      echo "         - WSL: .wslconfig [wsl2] nestedVirtualization=true then wsl --shutdown" >&2
    }
    virsh domcapabilities --virttype kvm >/dev/null 2>&1 || {
      echo "         - libvirt cannot use KVM — fix: sudo chown root:kvm /dev/kvm && sudo chmod 660 /dev/kvm" >&2
      echo "         - then: sudo usermod -aG kvm libvirt-qemu  (restart libvirtd if needed)" >&2
    }
    command -v virsh >/dev/null 2>&1 || echo "         - install libvirt: sudo apt install qemu-kvm libvirt-daemon-system libvirt-clients" >&2
    _in_libvirt_group || echo "         - sudo usermod -aG libvirt \"\$USER\" && newgrp libvirt (required even for root)" >&2
    _kvm2_libvirt_stack_ready || {
      if _is_wsl && ! _has_systemd; then
        echo "         - WSL: sudo virtlogd -d && sudo virtlockd -d && sudo libvirtd -d" >&2
      else
        echo "         - sudo systemctl start virtlogd virtlockd libvirtd" >&2
      fi
    }
    [[ "$(id -u)" -eq 0 ]] && echo "         - kvm2 as root needs minikube --force (script adds this)" >&2
  elif [[ "$driver" == none ]]; then
    echo "       none:" >&2
    echo "         - run as root with docker running" >&2
    echo "         - k8s 1.24+ needs containerd + /etc/containerd/config.toml + CNI plugins (script sets up)" >&2
    echo "         - WSL without systemd: containerd SystemdCgroup=false must match kubelet cgroupfs" >&2
    echo "         - none/WSL: mount --make-rshared / required for calico-node (script sets in preflight)" >&2
    echo "         - WSL without systemd: script installs /usr/local/sbin/systemctl shim" >&2
    _wsl_docker_host_mount_broken && {
      echo "         - Docker Desktop /Docker/host mount has unescaped spaces in /proc/mounts (kubelet dies)" >&2
      echo "         - script umounts /Docker/host in preflight; or disable WSL integration / reinstall Docker without spaces" >&2
    }
    echo "         - or enable systemd in /etc/wsl.conf: [boot] systemd=true" >&2
    echo "         - minikube delete -p ${MINIKUBE_PROFILE} if switching from a VM driver" >&2
  fi
  exit 1
}

resolve_minikube_cni() {
  local driver="${1:-${MINIKUBE_DRIVER:-}}"
  if [[ -n "${MINIKUBE_CNI:-}" ]]; then
    echo "$MINIKUBE_CNI"
    return 0
  fi
  # ponytail: Pixie eBPF path uses flannel; none driver keeps calico for legacy host kubelet
  case "$driver" in
    none) echo calico ;;
    *)    echo flannel ;;
  esac
}

ensure_minikube_ready() {
  require_minikube

  local driver cni
  driver=$(resolve_minikube_driver) || _minikube_driver_unavailable
  export MINIKUBE_DRIVER="$driver"
  cni=$(resolve_minikube_cni "$driver")
  export MINIKUBE_CNI="$cni"
  if [[ "$driver" == none ]] || [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
    _ensure_none_preflight
  fi

  if [[ "${SKIP_LOAD:-0}" -eq 0 ]]; then
    if [[ "$driver" == none ]]; then
      echo "==> Start Minikube (${MINIKUBE_PROFILE}, driver=none, host kernel $(uname -r), cni=${cni}, runtime=containerd)"
      echo "    (host kubelet + containerd; images build in host docker, load via minikube image load)"
    elif [[ "$driver" == kvm2 ]]; then
      _ensure_kvm2_libvirt_group
      _ensure_libvirtd_running
      echo "==> Start Minikube (${MINIKUBE_PROFILE}, driver=${driver}, memory=4096, cpus=2, cni=${cni})"
    else
      echo "==> Start Minikube (${MINIKUBE_PROFILE}, driver=${driver}, memory=4096, cpus=2, cni=${cni})"
    fi
    local -a start_args=(start --driver="$driver" --cni="$cni")
    if [[ "$driver" == none ]]; then
      start_args+=(--container-runtime=containerd)
      if _has_systemd; then
        start_args+=(--extra-config=kubelet.cgroup-driver=systemd)
      else
        start_args+=(--extra-config=kubelet.cgroup-driver=cgroupfs)
        swapon --show 2>/dev/null | grep -q . && \
          start_args+=(--extra-config=kubelet.fail-swap-on=false)
      fi
    else
      start_args+=(--memory=4096 --cpus=2)
    fi
    local err
    if ! err=$(_minikube_start "$driver" "${start_args[@]}" 2>&1); then
      _minikube_start_failed "$driver" "$err"
    fi
  else
    echo "==> Skip minikube start (--skip-load); assuming cluster is running (driver=${driver})"
    if [[ "$driver" == none ]]; then
      kubectl config use-context "$MINIKUBE_CONTEXT" >/dev/null 2>&1 || true
      if ! kubectl cluster-info --context "$MINIKUBE_CONTEXT" >/dev/null 2>&1; then
        echo "ERROR: kubectl cannot reach cluster (none driver — drop --skip-load or run minikube start)" >&2
        exit 1
      fi
    elif ! _minikube status --format='{{.Host}}' >/dev/null 2>&1; then
      echo "ERROR: Minikube profile '${MINIKUBE_PROFILE}' is not running (drop --skip-load or run minikube start)" >&2
      exit 1
    fi
  fi

  kubectl config use-context "$MINIKUBE_CONTEXT" >/dev/null 2>&1 || {
    echo "ERROR: kubectl context '${MINIKUBE_CONTEXT}' not found after minikube start" >&2
    exit 1
  }

  echo "==> Wait for Minikube API server (context=${MINIKUBE_CONTEXT})"
  local i
  for i in $(seq 1 30); do
    if kubectl cluster-info --context "$MINIKUBE_CONTEXT" >/dev/null 2>&1; then
      if [[ "$cni" == calico ]]; then
        _wait_calico_ready
      fi
      return 0
    fi
    echo "    API not ready yet (${i}/30)"
    sleep 2
  done

  echo "ERROR: kubectl cannot reach Minikube API (context=${MINIKUBE_CONTEXT})" >&2
  exit 1
}

use_minikube_docker_env() {
  if [[ "${MINIKUBE_DRIVER:-}" == none ]]; then
    echo "==> Use host docker (none driver — builds load into containerd via minikube image load)"
    return 0
  fi
  eval "$(_minikube docker-env)"
}

unload_minikube_docker_env() {
  [[ "${MINIKUBE_DRIVER:-}" == none ]] && return 0
  eval "$(_minikube docker-env -u 2>/dev/null || true)"
}

load_minikube_image() {
  local img="$1"
  [[ "${MINIKUBE_DRIVER:-}" == none ]] || return 0
  if ! docker image inspect "$img" >/dev/null 2>&1; then
    echo "ERROR: ${img} not in host docker — build it or drop --skip-build" >&2
    exit 1
  fi
  echo "==> Load ${img} into containerd (none driver)"
  _minikube image load "$img" || {
    command -v ctr >/dev/null 2>&1 || {
      echo "ERROR: ctr required to import images for none driver (apt install containerd)" >&2
      exit 1
    }
    docker save "$img" | ctr -n k8s.io images import -
  }
}

load_minikube_images() {
  local img
  for img in "$@"; do
    load_minikube_image "$img"
  done
}

# ponytail: self-check driver resolution without starting a cluster
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  _vbox_available && echo "virtualbox: ok" || echo "virtualbox: unavailable"
  if [[ -r /dev/kvm ]]; then echo "/dev/kvm: ok"; else echo "/dev/kvm: missing"; fi
  command -v virsh >/dev/null 2>&1 && echo "virsh: ok" || echo "virsh: missing (apt install libvirt-clients)"
  _in_libvirt_group && echo "libvirt group: ok" || echo "libvirt group: missing (script auto-adds when run as root)"
  _kvm2_libvirt_stack_ready && echo "libvirt stack: ok" || echo "libvirt stack: incomplete (need virtlogd, virtlockd, libvirtd)"
  _kvm2_available && echo "kvm2: ok" || echo "kvm2: unavailable"
  _none_ready && echo "none: ok (root + docker)" || echo "none: unavailable (needs root + docker)"
  _wsl_docker_host_mount_broken && echo "docker-desktop /Docker/host: broken (/proc/mounts >6 fields — script umounts)" || echo "docker-desktop /Docker/host: ok"
  command -v conntrack >/dev/null 2>&1 && echo "conntrack: ok" || echo "conntrack: missing"
  command -v crictl >/dev/null 2>&1 && echo "crictl: ok" || echo "crictl: missing (apt install cri-tools)"
  resolve_minikube_driver 2>/dev/null && echo "resolved: $(resolve_minikube_driver)" || echo "resolved: unavailable"
fi
