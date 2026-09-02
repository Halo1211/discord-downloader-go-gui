# Supported Sources

The downloader can process direct files, Discord attachments, and supported
linked media. Current resolvers include:

- Direct file links
- Discord attachments and CDN URLs
- Twitter/X
- Instagram
- Imgur
- Streamable
- Gfycat
- Tistory
- Flickr

Third-party resolvers may depend on unofficial page behavior and can stop
working when a provider changes its website or API. Prefer direct attachments
and public URLs where possible. Use a small test channel before enabling an
integration for a large history archive.

## Resolver order and practical guidance

The message handler examines Discord attachments and links, then applies the
selected source rule, filters, duplicate checks, and file-type switches before
writing. A direct Discord CDN attachment is the most predictable input. A
linked provider may require a public URL, a provider-specific credential, or a
working session cache under `cache/`.

Optional integrations are independent of the Discord bot token:

- Twitter/X may use account credentials, cookies, or a proxy.
- Instagram may use account credentials, TOTP, and a proxy.
- Flickr may use an API key.

Keep these values local. Provider authentication can expose private account
data and may stop working when the provider changes its API or page format.
Do not treat a resolver failure as evidence that Discord permissions are wrong;
test the same URL in a small channel and inspect the named resolver in the
application console.

## File categories

Use `saveImages`, `saveVideos`, `saveAudioFiles`, `saveTextFiles`, and
`saveOtherFiles` to control which content types are written. `save` is the
master switch. `filters.allowedDomains` and `filters.allowedExtensions` are
useful when a channel contains mixed links; an allow-list rejects values that
do not match it.
