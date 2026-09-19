#!/usr/bin/env bash
# Logs GPU power/temps/clocks once per second and fsyncs each line, so the
# last readings survive a hard crash. Run in a second terminal before ./start.sh:
#   ./gpu-monitor.sh            # writes gpu-monitor.log next to this script
#   ./gpu-monitor.sh /path/log  # custom log path
set -u
DEV=/sys/class/drm/card1/device
HW=$(ls -d "$DEV"/hwmon/hwmon* | head -1)
LOG="${1:-$(dirname "$0")/gpu-monitor.log}"
r() { cat "$1" 2>/dev/null || echo NA; }
printf 'time\tpower_W\tcap_W\tedge_C\tjunction_C\tmem_C\tvddgfx_mV\tsclk_MHz\tmclk_MHz\tfan_rpm\tgpu_busy_%%\tvram_used_MiB\tpcie\n' >> "$LOG"
echo "logging to $LOG (Ctrl-C to stop)"
while :; do
  line=$(printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s' \
    "$(date '+%H:%M:%S')" \
    "$(( $(r "$HW/power1_average" 2>/dev/null || r "$HW/power1_input") / 1000000 ))" \
    "$(( $(r "$HW/power1_cap") / 1000000 ))" \
    "$(( $(r "$HW/temp1_input") / 1000 ))" \
    "$(( $(r "$HW/temp2_input") / 1000 ))" \
    "$(( $(r "$HW/temp3_input") / 1000 ))" \
    "$(r "$HW/in0_input")" \
    "$(( $(r "$HW/freq1_input") / 1000000 ))" \
    "$(( $(r "$HW/freq2_input") / 1000000 ))" \
    "$(r "$HW/fan1_input")" \
    "$(r "$DEV/gpu_busy_percent")" \
    "$(( $(r "$DEV/mem_info_vram_used") / 1048576 ))" \
    "$(r "$DEV/current_link_speed")x$(r "$DEV/current_link_width")")
  echo "$line" >> "$LOG"
  sync -d "$LOG" 2>/dev/null || sync
  sleep 1
done
