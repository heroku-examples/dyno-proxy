# Single mode (no workers): the process that serves requests is the process
# /crash/app kills, so the whole server dies with it.
workers 0
threads 1, 5
port ENV.fetch("PORT", 5000)
environment ENV.fetch("RACK_ENV", "production")
