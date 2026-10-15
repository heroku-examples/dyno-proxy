# dyno-proxy e2e app (deployed by e2e/run.sh). Endpoints:
#   GET /              200 text with MARKER and the boot id
#   GET /boot          JSON {boot_id, pid, started_at, dyno, port}
#   GET /crash/app     202, then hard-exits this server process (status 137) after 0.5s
#   GET /crash/dyno-proxy 202 with the dyno-proxy pids found, then SIGKILLs them
#                      after 0.5s (409 if none are visible)
#   anything else      404 with NOT_FOUND, so the harness can tell the app
#                      answering directly (proxy inactive) from the probe route
require "json"
require "securerandom"
require "time"

MARKER = "dyno-proxy e2e app".freeze
NOT_FOUND = "dyno-proxy-e2e-app: not found".freeze
BOOT = {
  boot_id: SecureRandom.hex(8),
  pid: Process.pid,
  started_at: Time.now.utc.iso8601,
  dyno: ENV["DYNO"],
  port: ENV["PORT"]
}.freeze

# Pids of the dyno-proxy background daemon (`dyno-proxy background --foreground`),
# the process that owns Traefik. Its sentinel is deliberately left alone: the
# sentinel is what restarts the dyno when the daemon dies.
def dyno_proxy_pids(proc_root = "/proc")
  Dir.glob("#{proc_root}/[0-9]*").filter_map do |dir|
    pid = File.basename(dir).to_i
    next if pid == Process.pid
    comm = begin
      File.read("#{dir}/comm").strip
    rescue SystemCallError
      next
    end
    argv = begin
      File.read("#{dir}/cmdline").split("\0")
    rescue SystemCallError
      []
    end
    daemon = comm.start_with?("dyno-proxy") || File.basename(argv.first.to_s).start_with?("dyno-proxy")
    pid if daemon && argv.include?("--foreground")
  end
end

def later
  Thread.new do
    sleep 0.5
    yield
  end
end

def text(status, body)
  [status, {"content-type" => "text/plain", "cache-control" => "no-store"}, ["#{body}\n"]]
end

def json(status, obj)
  [status, {"content-type" => "application/json", "cache-control" => "no-store"}, ["#{JSON.generate(obj)}\n"]]
end

run lambda { |env|
  case [env["REQUEST_METHOD"], env["PATH_INFO"]]
  when ["GET", "/"]
    text(200, "#{MARKER} boot_id=#{BOOT[:boot_id]}")
  when ["GET", "/boot"]
    json(200, BOOT)
  when ["GET", "/crash/app"]
    $stdout.puts "dyno-proxy-e2e-app: /crash/app: hard exit of pid #{Process.pid} in 0.5s"
    $stdout.flush
    # exit!, not a self-SIGKILL: as PID 1 (Fir CNB) the kernel drops signals a
    # namespace init sends itself, so the kill would be a no-op.
    later { Process.exit!(137) }
    json(202, {crashing: "app", pid: Process.pid, boot_id: BOOT[:boot_id]})
  when ["GET", "/crash/dyno-proxy"]
    pids = dyno_proxy_pids
    $stdout.puts "dyno-proxy-e2e-app: /crash/dyno-proxy: SIGKILL pids #{pids.inspect} in 0.5s"
    $stdout.flush
    later do
      pids.each do |pid|
        Process.kill(:KILL, pid)
      rescue SystemCallError => e
        $stdout.puts "dyno-proxy-e2e-app: kill #{pid}: #{e.message}"
      end
    end
    json(pids.empty? ? 409 : 202, {crashing: "dyno-proxy", pids: pids, boot_id: BOOT[:boot_id]})
  else
    text(404, NOT_FOUND)
  end
}
