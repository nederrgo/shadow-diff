import sys
import os
from datetime import datetime

if len(sys.argv) < 2:
    print("Error: Missing log message.")
    sys.exit(1)

msg = sys.argv[1]
log_path = "docs/log.md"

if not os.path.exists(log_path):
    print(f"Error: {log_path} not found.")
    sys.exit(1)

with open(log_path, "r", encoding="utf-8") as f:
    content = f.read()

# Separate the OKF frontmatter from the Markdown body
try:
    # Splits by the closing frontmatter delimiter
    frontmatter, body = content.split("---\n", 2)[1:]
    frontmatter = f"---\n{frontmatter}---\n"
except ValueError:
    print("Error: Could not parse OKF frontmatter structure.")
    sys.exit(1)

today = datetime.now().strftime("%Y-%m-%d")
date_header = f"## [{today}]"
bullet_point = f"* {msg}\n"

# Check if today's log entry block already exists
if date_header in body:
    # Target the '### Added' section directly underneath today's date
    target_section = f"{date_header}\n### Added\n"
    if target_section in body:
        body = body.replace(target_section, f"{target_section}{bullet_point}")
    else:
        # Fallback if '### Added' heading was missing for today
        body = body.replace(date_header, f"{date_header}\n### Added\n{bullet_point}")
else:
    # Create a fresh date block right under the main title header
    title_marker = "# Shadow-Diff Documentation Log\n"
    if title_marker in body:
        new_block = f"{title_marker}\n{date_header}\n### Added\n{bullet_point}"
        body = body.replace(title_marker, new_block)
    else:
        # Safety fallback if title changes
        body = f"\n{date_header}\n### Added\n{bullet_point}\n" + body

# Reconstruct the file safely
with open(log_path, "w", encoding="utf-8") as f:
    f.write(frontmatter + body)

print(f"✓ Successfully appended to {today} section in docs/log.md")