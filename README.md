# downloadmanagersquared (DMS²)

Downloads large files over **all your network connections at once** (Ethernet, Wi‑Fi, a phone hotspot, …), so their speeds add up. It also works with sites that give each IP address its own download link: DMS² opens the site once through each connection in Firefox and takes the link the site makes for that connection.

Windows 10/11, with Firefox or a Firefox-based browser (LibreWolf, Waterfox, Floorp). HTTP(S) downloads and torrents.

## Install

1. Download `downloadmanagersquared-windows.zip` from the [latest release](https://github.com/unicrncndms/downloadmanagersquared/releases/latest) and **extract the whole zip** to a folder.
2. Run `dms-engine.exe`. Windows SmartScreen warns about it, because the program isn't code-signed: click **More info → Run anyway**.

   The engine installs itself for your Windows user only (no admin rights) into `%LOCALAPPDATA%\downloadmanagersquared`, starts with Windows and runs in the tray, next to the clock. You can delete the extracted folder afterwards.
3. A message then offers to open the Firefox extension. Click OK and let Firefox install it. Using LibreWolf, Waterfox or Floorp? Open the link it shows in that browser instead:
   `https://github.com/unicrncndms/downloadmanagersquared/releases/latest/download/downloadmanagersquared.xpi`

The extension asks to enable containers: DMS² uses one per connection while it gets a site's link through it.

## Your first download

Download a file of 100 MB or more in Firefox as usual. DMS² takes it over and fetches it over every connection you have. Click the DMS² icon in the toolbar to see your downloads; **Connections & settings** at the bottom of that list holds the rest.

If the site gives each IP its own link, DMS² needs to learn how to get one:

1. The first time, a window opens with a blue bar at the bottom. Click through the site as you normally would, until the download starts. DMS² remembers your clicks for that site.
2. From then on, DMS² clicks through by itself in a minimized window, for each connection, as soon as the site's buttons are ready. If the site changes, the window comes back and asks you to click through once more.

Each such window **starts without cookies**: you aren't logged in there. A site that only lets logged-in users download can't be clicked through automatically.

Settings worth knowing (all under **Connections & settings**):

- **Connections**: turn each one on or off, set how many parallel requests it gets, limit its speed, and choose the **main connection** (used alone for servers that can't split a file). VPN adapters start off.
- **Settings**: the size from which downloads are taken over (100 MB), the save folder (your Downloads folder), how many downloads run at once (1), and start with Windows.
- **Learned sites**: the clicks DMS² remembers per site; *Forget* teaches a site again.

Unfinished files end in `.dms.part` and are renamed when done. Progress is saved every few seconds, so downloads continue after a crash, sleep or restart.

## Upgrade

Run the `dms-engine.exe` of a newer zip, as when installing. It replaces the installed engine and keeps your downloads and settings. The extension updates itself through Firefox.

## Uninstall

*Settings → Apps → Installed apps → downloadmanagersquared → Uninstall.* Your downloaded files are always kept, including unfinished ones; you choose whether settings and the download list go too. Then remove the extension in Firefox under *Add-ons and themes*.

## Who it protects you from

The engine only takes requests from your own Windows user: other people's accounts on the same PC can't use its pipe or its proxies, and its proxies only connect to the internet, never to your local network. It does **not** protect you from programs running as your own user, or from administrators of the PC. See [PRIVACY.md](PRIVACY.md) for what DMS² does with your data.

## How it fits together

```
Firefox extension ──native messaging──> dms-host.exe ──named pipe──> dms-engine.exe (tray)
   downloads list, settings page            (relay)                     downloads, queue, torrents
   takes over big downloads                                             one SOCKS5 proxy per connection
   gets per-IP links in containers   ───── container traffic ─────>     (pages load via that connection)
```

- **Engine** (`engine/`, Go): finds the connections that have their own route to the internet and pins sockets to each one. It splits a file into chunks; when a connection finishes its chunk, it takes over the far end of the slowest one, so faster connections carry more and nothing is fetched twice.
- **Extension** (`extension/`, plain JavaScript, no build step): takes over downloads, holds the UI, and resolves per-IP links in a temporary container per connection whose traffic goes through the engine's proxy for that connection.

## Build from source

Needs Go (`winget install GoLang.Go`) and, for the extension's tests, Node.js.

```bash
powershell -ExecutionPolicy Bypass -File build.ps1
```

It runs the tests and writes `dist\dms-engine.exe`, `dist\dms-host.exe`, `dist\downloadmanagersquared.xpi` (unsigned) and the release zip. Run `dist\dms-engine.exe` to install that build. Release Firefox only keeps signed extensions; to try an unsigned one, load `extension\manifest.json` under `about:debugging` → *This Firefox* → *Load Temporary Add-on*.

Tests on their own:

```bash
cd engine && go test ./...
```

```bash
node --test extension/test
```

`extension/dev/` has a harness for the popup and settings page (`python extension/dev/serve.py 8765`) and a fixture file host to test resolving against (`python extension/dev/landing.py 8780`).

## Files and logs

- Settings, download list and log: `%APPDATA%\downloadmanagersquared\` (`settings.json`, `downloads\`, `torrents\`, `engine.log`). A file that can't be read is renamed to `….broken` and you get a notification.
- The installed programs: `%LOCALAPPDATA%\downloadmanagersquared\`.

## Known limitations

- **IPv4 only.** Each connection is used through its IPv4 address.
- **Replayed clicks are synthetic.** A site that only accepts real mouse clicks asks you to click every time.
- Download links a site creates through a form POST can't be fetched again by the engine.
- Some servers refuse anything but a browser. The engine sends your browser's User-Agent, but servers that fingerprint TLS may still refuse it.

## Licence

MIT, see [LICENSE](LICENSE). The release zip's `THIRD-PARTY-NOTICES` lists the licences of the libraries in the engine.
