package capture

import "path/filepath"

// generatedEtcPatterns match files under /etc that the system writes itself
// or that capture handles elsewhere, so their presence says nothing about
// what a person changed. Patterns are matched with path.Match against the
// path relative to the root, with a leading slash.
var generatedEtcPatterns = []string{
	// Alternatives, init and unit wiring.
	"/etc/alternatives/*", "/etc/rc?.d/*", "/etc/systemd/system/*.wants/*", "/etc/systemd/system/*.requires/*",
	"/etc/init.d/.depend.*",
	// Account databases and their backups.
	"/etc/passwd", "/etc/passwd-", "/etc/group", "/etc/group-", "/etc/shadow", "/etc/shadow-",
	"/etc/gshadow", "/etc/gshadow-", "/etc/subuid", "/etc/subuid-", "/etc/subgid", "/etc/subgid-",
	"/etc/.pwd.lock", "/etc/sudoers.d/*",
	// Machine identity, network and mounts.
	"/etc/hostname", "/etc/hosts", "/etc/machine-id", "/etc/resolv.conf", "/etc/.resolv.conf*", "/etc/mtab",
	"/etc/fstab", "/etc/adjtime", "/etc/networks", "/etc/wsl.conf", "/etc/wsl-distribution.conf",
	"/etc/ssh/ssh_host_*", "/etc/netplan/*", "/etc/cloud/*", "/etc/landscape/*",
	// Locale, timezone, console.
	"/etc/localtime", "/etc/timezone", "/etc/default/locale", "/etc/locale.gen", "/etc/locale.alias",
	"/etc/locale.conf", "/etc/environment", "/etc/console-setup/*", "/etc/inputrc", "/etc/modules",
	// Caches and compiled data.
	"/etc/ld.so.cache", "/etc/.updated", "/etc/udev/hwdb.bin", "/etc/apparmor.d/cache/*", "/etc/apparmor.d/cache.d/*",
	"/etc/apparmor.d/local/*", "/etc/apparmor.d/tunables/*/*", "/etc/python3*/*", "/etc/fonts/conf.d/*",
	"/etc/ssl/certs/*", "/etc/ca-certificates.conf", "/etc/ca-certificates.conf.*", "/etc/mailcap", "/etc/xml/*", "/etc/sgml/*",
	// dpkg's own leftovers from upgrades.
	"/etc/*.dpkg-old", "/etc/*/*.dpkg-old", "/etc/*.dpkg-dist", "/etc/*/*.dpkg-dist", "/etc/*.dpkg-new", "/etc/*/*.dpkg-new",
	"/etc/*.ucf-old", "/etc/*/*.ucf-old", "/etc/*.ucf-dist", "/etc/*/*.ucf-dist",
	// apt configuration, reported separately as sources.
	"/etc/apt/sources.list", "/etc/apt/sources.list.d/*", "/etc/apt/trusted.gpg.d/*", "/etc/apt/keyrings/*",
	"/etc/apt/apt.conf.d/*", "/etc/apt/trusted.gpg",
	// Release and defaults Ubuntu writes.
	"/etc/os-release", "/etc/lsb-release", "/etc/issue*", "/etc/legal", "/etc/motd", "/etc/shells",
	"/etc/rsyslog.d/50-default.conf", "/etc/profile", "/etc/nsswitch.conf", "/etc/ld.so.conf.d/*",
	// Written by package configuration tools (pam-auth-update, ucf, cloud-init).
	"/etc/pam.d/*", "/etc/security/opasswd", "/etc/default/*", "/etc/cloud/*", "/etc/cloud/*/*",
	// Reported under services and scheduled jobs instead.
	"/etc/systemd/system/*", "/etc/systemd/user/*", "/etc/cron.d/*", "/etc/cron.*/*",
}

// isGeneratedEtcPath reports whether a path (relative to the root, with a
// leading slash) is one the system generates.
func isGeneratedEtcPath(path string) bool {
	for _, pattern := range generatedEtcPatterns {
		if matched, err := filepath.Match(pattern, path); err == nil && matched {
			return true
		}
	}
	return false
}
