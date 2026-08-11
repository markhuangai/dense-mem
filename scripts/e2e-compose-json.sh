json_field() {
  local field="$1"
  node -e '
let input = "";
process.stdin.on("data", chunk => input += chunk);
process.stdin.on("end", () => {
  const value = process.argv[1]
    .split(".")
    .reduce((current, key) => current?.[key], JSON.parse(input));
  if (value === undefined || value === null || value === "") {
    process.exit(1);
  }
  process.stdout.write(String(value));
});
' "$field"
}
