#!/usr/bin/env bash
set -euo pipefail
prefix=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
binary="$prefix/bin/dualsync"
media_root= listen=127.0.0.1:8090 service_user=dualsync service_name=dualsync
config=/etc/dualsync/config.json state=/var/lib/dualsync
external=false allow=false noninteractive=false no_start=false
service_user_set=false
while (($#)); do
  case "$1" in
    --media-root) media_root=$2; shift 2;;
    --listen) listen=$2; shift 2;;
    --service-user) service_user=$2; service_user_set=true; shift 2;;
    --service-name) service_name=$2; shift 2;;
    --config) config=$2; shift 2;;
    --state-dir) state=$2; shift 2;;
    --no-webui) external=true; shift;;
    --allow-unsupported) allow=true; shift;;
    --noninteractive) noninteractive=true; shift;;
    --no-start) no_start=true; shift;;
    *) printf 'Unknown option: %s\n' "$1" >&2; exit 2;;
  esac
done
[[ $EUID == 0 ]] || { echo 'Run install.sh as root.' >&2; exit 1; }
[[ $service_name =~ ^[a-zA-Z0-9_-]+$ && $service_user =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || { echo 'Invalid service name/account.' >&2; exit 1; }
if ! $service_user_set && [[ -f /etc/systemd/system/$service_name.service ]]; then
  previous_user=$(sed -n 's/^User=//p' "/etc/systemd/system/$service_name.service" | head -n 1)
  service_user=${previous_user:-$service_user}
fi
validate_state() {
  case "$(realpath -m -- "$1")" in
    /|/etc|/var|/var/lib|/usr|/usr/local|/opt|/home|/srv|/run|/tmp|/var/tmp)
      echo 'Use a dedicated state directory, not a shared system directory.' >&2; exit 1;;
  esac
}
validate_state "$state"
[[ -x $binary ]] || { echo 'Backend binary missing or not executable.' >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo 'Install FFmpeg with libass and a supported H.264 encoder.' >&2; exit 1; }
command -v ffprobe >/dev/null || { echo 'Install ffprobe from the FFmpeg package.' >&2; exit 1; }
command -v systemctl >/dev/null || { echo 'systemd is required by this installer.' >&2; exit 1; }
if ! $noninteractive && [[ ! -f $config ]]; then
  read -r -p 'Media root: ' media_root
  read -r -p "Listen address [$listen]: " answer; listen=${answer:-$listen}
  read -r -p "Service account [$service_user]: " answer; service_user=${answer:-$service_user}
  read -r -p 'Serve frontend internally? [Y/n]: ' answer; [[ ${answer,,} != n ]] || external=true
fi
[[ $service_user =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || { echo 'Invalid service account.' >&2; exit 1; }
if ! id "$service_user" >/dev/null 2>&1; then useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$service_user"; fi
service_group=$(id -gn "$service_user")
for group in video render; do getent group "$group" >/dev/null && usermod -a -G "$group" "$service_user" || true; done
config_dir=$(dirname -- "$config")
if [[ ! -d $config_dir ]]; then install -d -m 0750 -o root -g "$service_group" "$config_dir"; fi
install -d -m 0700 -o "$service_user" -g "$service_group" "$state"
runuser -u "$service_user" -- test -x "$binary" || { echo 'Service account cannot access extracted installation. Grant directory traversal/read access.' >&2; exit 1; }
args=(configure --service-user "$service_user" --unit-output "/etc/systemd/system/$service_name.service")
if [[ -f $config ]]; then
  echo 'Preserving existing configuration.'
  args+=(--config "$config")
else
  [[ -n $media_root ]] || { echo '--media-root required for noninteractive installation.' >&2; exit 1; }
  runuser -u "$service_user" -- test -r "$media_root" || { echo 'Service account cannot read media root.' >&2; exit 1; }
  args+=(--media-root "$media_root" --listen "$listen" --state-dir "$state" --output "$config")
  $external && args+=(--no-webui)
  $allow && args+=(--allow-unsupported)
fi
"$binary" "${args[@]}"
chown root:"$service_group" "$config"; chmod 0640 "$config"
state=$("$binary" configure --config "$config" --print-state-dir)
validate_state "$state"
install -d -m 0700 -o "$service_user" -g "$service_group" "$state"
runuser -u "$service_user" -- "$binary" configure --config "$config"
runuser -u "$service_user" -- "$binary" doctor --config "$config" --encoding-test --output "$state/diagnostics.json"
if ! grep -q '"supported": true' "$state/diagnostics.json"; then
  if ! $allow; then
    if $noninteractive; then echo 'Unsupported host. Explicit --allow-unsupported required.' >&2; exit 1; fi
    read -r -p 'Host unsupported. Install anyway? Type unsupported: ' answer
    [[ $answer == unsupported ]] || exit 1
    allow=true
  fi
fi
if $allow; then
  "$binary" configure --config "$config" --allow-unsupported --output "$config" --service-user "$service_user" --unit-output "/etc/systemd/system/$service_name.service"
  chown root:"$service_group" "$config"; chmod 0640 "$config"
fi
systemd-analyze verify "/etc/systemd/system/$service_name.service"
systemctl daemon-reload
if ! $no_start; then systemctl enable --now "$service_name.service"; fi
printf 'Installed in %s. Publishing starts manually through the API.\n' "$prefix"
