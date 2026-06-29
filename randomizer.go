package main

import (
	"fmt"
	"math/rand"
	"net/http"
)

// ── Dynamic browser fingerprint generator ──
// Every request gets a completely randomized set of headers so the upstream
// cannot fingerprint us by UA, IP, or browser feature set.

// ── OS / platform helpers ──

type osInfo struct {
	os       string // "Windows", "Mac", "Linux"
	platform string // for Sec-Ch-Ua-Platform
}

var osList = []osInfo{
	{os: "Windows", platform: "Windows"},
	{os: "Mac", platform: "macOS"},
	{os: "Mac", platform: "macOS"},
	{os: "Linux", platform: "Linux"},
}

func randomOS() osInfo {
	return osList[rand.Intn(len(osList))]
}

// ── Version generators ──

func randRange(min, max int) int {
	if min >= max {
		return min
	}
	return min + rand.Intn(max-min+1)
}

type chromeVer struct {
	major int
	minor int
	build int
	patch int
}

func randomChromeVer() chromeVer {
	return chromeVer{
		major: randRange(90, 134),
		minor: randRange(0, 9),
		build: randRange(1000, 9999),
		patch: randRange(0, 199),
	}
}

type firefoxVer struct {
	major int
}

func randomFirefoxVer() firefoxVer {
	return firefoxVer{major: randRange(70, 138)}
}

type safariVer struct {
	major   int
	minor   int
	webKit  string // e.g. "605.1.15"
}

func randomSafariVer() safariVer {
	wkMajors := []string{"605", "606", "607", "608"}
	return safariVer{
		major:  randRange(15, 18),
		minor:  randRange(0, 6),
		webKit: fmt.Sprintf("%s.%d.%d", wkMajors[rand.Intn(len(wkMajors))], randRange(1, 40), randRange(1, 20)),
	}
}

// ── User-Agent generators ──

func chromeUA(os osInfo, v chromeVer) string {
	webKit := "537.36"
	switch os.os {
	case "Windows":
		return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/%s (KHTML, like Gecko) Chrome/%d.%d.%d.%d Safari/%s",
			webKit, v.major, v.minor, v.build, v.patch, webKit)
	case "Mac":
		osxPatch := randRange(1, 7)
		return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_%d) AppleWebKit/%s (KHTML, like Gecko) Chrome/%d.%d.%d.%d Safari/%s",
			osxPatch, webKit, v.major, v.minor, v.build, v.patch, webKit)
	default: // Linux
		return fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/%s (KHTML, like Gecko) Chrome/%d.%d.%d.%d Safari/%s",
			webKit, v.major, v.minor, v.build, v.patch, webKit)
	}
}

func edgeUA(os osInfo, v chromeVer) string {
	return chromeUA(os, v) + fmt.Sprintf(" Edg/%d.%d.%d.%d", v.major, v.minor, v.build, v.patch)
}

func operaUA(os osInfo, v chromeVer) string {
	// Opera uses Chrome engine, version usually 10-20 behind
	opVer := randRange(v.major-20, v.major-5)
	if opVer < 70 {
		opVer = 70 + rand.Intn(30)
	}
	return chromeUA(os, v) + fmt.Sprintf(" OPR/%d.0.%d.%d", opVer, randRange(2000, 9999), randRange(0, 999))
}

func firefoxUA(os osInfo, v firefoxVer) string {
	switch os.os {
	case "Windows":
		return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:%d.0) Gecko/20100101 Firefox/%d.0", v.major, v.major)
	case "Mac":
		return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:%d.0) Gecko/20100101 Firefox/%d.0", v.major, v.major)
	default:
		return fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64; rv:%d.0) Gecko/20100101 Firefox/%d.0", v.major, v.major)
	}
}

func safariUA(os osInfo, v safariVer) string {
	osxPatch := randRange(1, 7)
	return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_%d) AppleWebKit/%s (KHTML, like Gecko) Version/%d.%d Safari/%s",
		osxPatch, v.webKit, v.major, v.minor, v.webKit)
}

// generateUA picks a random browser engine and generates a realistic UA string.
func generateUA() string {
	os := randomOS()
	switch rand.Intn(6) {
	case 0, 1, 2: // Chrome 50%
		return chromeUA(os, randomChromeVer())
	case 3: // Edge 17%
		return edgeUA(os, randomChromeVer())
	case 4: // Firefox 17%
		return firefoxUA(os, randomFirefoxVer())
	case 5: // Opera or Safari 16%
		if rand.Intn(2) == 0 {
			return operaUA(os, randomChromeVer())
		}
		return safariUA(os, randomSafariVer())
	default:
		return chromeUA(os, randomChromeVer())
	}
}

// ── Sec-Ch-Ua generator ──

func generateSecChUa(major int) string {
	brands := []string{
		`"Not)A;Brand"`,
		`"Not/A=Brand"`,
		`"Not|A_Brand"`,
		`"Not;A=Brand"`,
		`"Chromium"`,
		`"Google Chrome"`,
	}
	// Pick 3 random brands: usually includes "Google Chrome", "Chromium", and one fake
	perm := rand.Perm(len(brands))
	selected := make([]string, 0, 3)
	for i := 0; i < 3 && i < len(perm); i++ {
		name := brands[perm[i]]
		ver := major
		if perm[i] >= 3 { // fake brands get a different version
			ver = randRange(8, 99)
		} else if perm[i] == 4 { // Chromium
			ver = major
		} else if perm[i] == 5 { // Google Chrome
			ver = major
		}
		selected = append(selected, fmt.Sprintf(`%s;v="%d"`, name, ver))
	}
	return fmt.Sprintf(`%s, %s, %s`, selected[0], selected[1], selected[2])
}

func generateSecChUaFullVersion(major int) string {
	return fmt.Sprintf(`"%d.%d.%d.%d"`, major, randRange(0, 9), randRange(1000, 9999), randRange(0, 199))
}

// ── Accept-Language ──

var acceptLanguages = []string{
	"zh-CN,zh;q=0.9,en;q=0.8",
	"zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7",
	"zh-TW,zh;q=0.9,en;q=0.8",
	"zh-HK,zh;q=0.9,en;q=0.8",
	"en-US,en;q=0.9,zh-CN;q=0.8",
	"en-US,en;q=0.9,zh;q=0.8",
	"en-US,en;q=0.9",
	"en-GB,en;q=0.9,zh-CN;q=0.8",
	"en-GB,en;q=0.9",
	"ja-JP,ja;q=0.9,en;q=0.8",
	"ko-KR,ko;q=0.9,en;q=0.8",
}

// ── Accept variants ──

var acceptValues = []string{
	"text/event-stream, application/json, text/plain, */*",
	"text/event-stream, application/json, text/html, */*",
	"text/event-stream, */*",
	"application/json, text/plain, */*",
	"*/*",
}

// ── IP generators ──

func randomPrivateIP() string {
	switch rand.Intn(3) {
	case 0:
		return fmt.Sprintf("10.%d.%d.%d", rand.Intn(256), rand.Intn(256), rand.Intn(256))
	case 1:
		return fmt.Sprintf("192.168.%d.%d", rand.Intn(256), rand.Intn(256))
	default:
		return fmt.Sprintf("172.%d.%d.%d", 16+rand.Intn(16), rand.Intn(256), rand.Intn(256))
	}
}

func randomPublicIP() string {
	for {
		a := rand.Intn(256)
		b := rand.Intn(256)
		c := rand.Intn(256)
		d := rand.Intn(256)
		if a == 0 || a == 10 || a == 127 || a == 169 || a == 172 || a == 192 || a >= 224 {
			continue
		}
		if a == 100 && b >= 64 && b <= 127 {
			continue
		}
		return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d)
	}
}

func randomMobileUA() string {
	// Occasionally generate a mobile UA
	androidVer := randRange(10, 15)
	chromeMajor := randRange(90, 134)
	build := randRange(1000, 9999)
	model := fmt.Sprintf("SM-%c%d%c", rune('A'+rand.Intn(26)), randRange(100, 999), rune('A'+rand.Intn(26)))
	return fmt.Sprintf("Mozilla/5.0 (Linux; Android %d; %s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.%d.0 Mobile Safari/537.36",
		androidVer, model, chromeMajor, build)
}

// setRandomHeaders randomizes every fingerprintable header on the request.
func setRandomHeaders(req *http.Request) {
	// ── Decide desktop vs mobile (10% mobile) ──
	isMobile := rand.Intn(10) == 0

	// ── User-Agent ──
	if isMobile {
		req.Header.Set("User-Agent", randomMobileUA())
	} else {
		req.Header.Set("User-Agent", generateUA())
	}

	// ── Accept & Accept-Language ──
	req.Header.Set("Accept", acceptValues[rand.Intn(len(acceptValues))])
	req.Header.Set("Accept-Language", acceptLanguages[rand.Intn(len(acceptLanguages))])

	// Accept-Encoding: vary or omit
	if rand.Intn(4) > 0 {
		req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	} else {
		req.Header.Set("Accept-Encoding", "gzip, deflate")
	}

	// ── Cache control ──
	cacheCtrls := []string{"no-cache", "no-store", "max-age=0", ""}
	if val := cacheCtrls[rand.Intn(len(cacheCtrls))]; val != "" {
		req.Header.Set("Cache-Control", val)
	} else {
		req.Header.Del("Cache-Control")
	}

	// ── IP headers (proxy chaining simulation) ──
	fwdIP := randomPublicIP()
	clientIP := randomPrivateIP()
	existing := req.Header.Get("X-Forwarded-For")
	if existing != "" && rand.Intn(2) == 0 {
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("%s, %s, %s", fwdIP, clientIP, existing))
	} else {
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("%s, %s", fwdIP, clientIP))
	}
	req.Header.Set("X-Real-IP", clientIP)
	req.Header.Set("X-Client-IP", clientIP)
	req.Header.Set("X-Forwarded-Host", "chatjimmy.ai")
	req.Header.Set("X-Forwarded-Proto", "https")

	// Extra IP headers that proxies/CDNs sometimes add
	if rand.Intn(2) == 0 {
		req.Header.Set("True-Client-IP", clientIP)
	}
	if rand.Intn(3) == 0 {
		req.Header.Set("CF-Connecting-IP", randomPublicIP())
	}
	if rand.Intn(4) == 0 {
		req.Header.Set("X-Request-ID", fmt.Sprintf("req-%x-%x", rand.Int63(), rand.Int63()))
	}

	// ── Sec-CH-UA (client hints) ──
	if !isMobile {
		major := randRange(90, 134)
		platform := "Windows"
		platVer := "15.0.0"
		switch rand.Intn(4) {
		case 0:
			platform = "Windows"
			platVer = fmt.Sprintf("%d.0.0", randRange(10, 11))
		case 1:
			platform = "macOS"
			platVer = fmt.Sprintf("%d.%d.%d", randRange(13, 15), randRange(0, 6), randRange(0, 9))
		case 2:
			platform = "Linux"
			platVer = "6.8.0"
		case 3:
			platform = "Chrome OS"
			platVer = fmt.Sprintf("%d.0.0", randRange(120, 130))
		}

		req.Header.Set("Sec-Ch-Ua", generateSecChUa(major))
		req.Header.Set("Sec-Ch-Ua-Platform", fmt.Sprintf("%q", platform))
		req.Header.Set("Sec-Ch-Ua-Platform-Version", fmt.Sprintf("%q", platVer))
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Bitness", `"64"`)
		req.Header.Set("Sec-Ch-Ua-Full-Version", generateSecChUaFullVersion(major))
		if rand.Intn(2) == 0 {
			req.Header.Set("Sec-Ch-Ua-Model", `""`)
		}
		if rand.Intn(3) == 0 {
			req.Header.Set("Sec-Ch-Ua-Arch", `"x86"`)
		} else if rand.Intn(2) == 0 {
			req.Header.Set("Sec-Ch-Ua-Arch", `"arm"`)
		}
	} else {
		// Mobile client hints
		major := randRange(90, 134)
		models := []string{`"Pixel 9"`, `"SM-S25"`, `"iPhone16,2"`, `"Xiaomi14"`, `"OPPO Find X8"`}
		req.Header.Set("Sec-Ch-Ua", generateSecChUa(major))
		req.Header.Set("Sec-Ch-Ua-Platform", `"Android"`)
		req.Header.Set("Sec-Ch-Ua-Platform-Version", fmt.Sprintf(`"%d.0.0"`, randRange(10, 15)))
		req.Header.Set("Sec-Ch-Ua-Mobile", "?1")
		req.Header.Set("Sec-Ch-Ua-Model", models[rand.Intn(len(models))])
		req.Header.Set("Sec-Ch-Ua-Bitness", `"64"`)
	}

	// ── Sec-Fetch-* ──
	fetchSites := []string{"same-origin", "same-site", "cross-site", "none"}
	fetchModes := []string{"cors", "no-cors", "navigate"}
	fetchDests := []string{"empty", "document", "iframe", "script", "style"}
	req.Header.Set("Sec-Fetch-Site", fetchSites[rand.Intn(len(fetchSites))])
	req.Header.Set("Sec-Fetch-Mode", fetchModes[rand.Intn(len(fetchModes))])
	req.Header.Set("Sec-Fetch-Dest", fetchDests[rand.Intn(len(fetchDests))])
	if rand.Intn(2) == 0 {
		req.Header.Set("Sec-Fetch-User", "?1")
	}

	// ── Network / connection hints ──
	if rand.Intn(2) == 0 {
		req.Header.Set("DNT", "1")
	} else {
		req.Header.Del("DNT")
	}
	if rand.Intn(2) == 0 {
		req.Header.Set("Save-Data", "on")
	} else {
		req.Header.Del("Save-Data")
	}

	// Viewport / device hints
	widths := []string{"1920", "1440", "1366", "1536", "1680", "1280", "1600", "2560", "1080"}
	if isMobile {
		mobileWidths := []string{"360", "375", "390", "393", "412", "414", "430"}
		req.Header.Set("Viewport-Width", mobileWidths[rand.Intn(len(mobileWidths))])
		req.Header.Del("Device-Memory")
	} else {
		if rand.Intn(2) == 0 {
			req.Header.Set("Viewport-Width", widths[rand.Intn(len(widths))])
		} else {
			req.Header.Del("Viewport-Width")
		}
		if rand.Intn(3) == 0 {
			memories := []string{"0.25", "0.5", "1", "2", "4", "8"}
			req.Header.Set("Device-Memory", memories[rand.Intn(len(memories))])
		}
	}

	// RTT / Downlink / ECT (network quality)
	if rand.Intn(3) == 0 {
		rtts := []string{"50", "100", "150", "200", "250", "300"}
		req.Header.Set("RTT", rtts[rand.Intn(len(rtts))])
		downlinks := []string{"1.0", "1.5", "2.0", "3.0", "5.0", "10.0"}
		req.Header.Set("Downlink", downlinks[rand.Intn(len(downlinks))])
		ects := []string{"4g", "3g", "slow-2g"}
		req.Header.Set("ECT", ects[rand.Intn(len(ects))])
	}

	// ── Priority ──
	if rand.Intn(2) == 0 {
		priorities := []string{"u=1", "u=1, i", "u=0", "u=2"}
		req.Header.Set("Priority", priorities[rand.Intn(len(priorities))])
	}

	// ── Purpose / X-Purpose (prefetch detection) ──
	if rand.Intn(5) == 0 {
		req.Header.Set("Purpose", "prefetch")
		req.Header.Set("X-Purpose", "preview")
	}

	// ── Via / X-Cache (proxy simulation) ──
	if rand.Intn(4) == 0 {
		cacheStatus := []string{"HIT", "MISS", "BYPASS"}
		req.Header.Set("X-Cache", fmt.Sprintf("%s from cloudfront", cacheStatus[rand.Intn(len(cacheStatus))]))
	}
	if rand.Intn(5) == 0 {
		req.Header.Set("Via", fmt.Sprintf("1.1 varnish-v%d", randRange(1, 99)))
	}

	// ── TE (trailer expected) ──
	if rand.Intn(3) == 0 {
		req.Header.Set("TE", "trailers")
	}
}
