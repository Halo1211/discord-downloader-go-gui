package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Davincible/goinsta/v3"
	"github.com/PuerkitoBio/goquery"
	"github.com/bwmarrin/discordgo"
	"github.com/fatih/color"
	twitterscraper "github.com/imperatrona/twitter-scraper"
)

const (
	imgurClientID   = "08af502a9e70d65"
	sneakyUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0"
)

func ensureSessionCacheDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0700)
}

func exportTwitterSession() error {
	if twitterScraper == nil {
		return errors.New("Twitter scraper is not initialized")
	}
	if err := ensureSessionCacheDir(pathCacheTwitter); err != nil {
		return err
	}
	cookies, err := json.Marshal(twitterScraper.GetCookies())
	if err != nil {
		return err
	}
	return os.WriteFile(pathCacheTwitter, cookies, 0600)
}

func exportInstagramSession() error {
	if instagramClient == nil {
		return errors.New("Instagram client is not initialized")
	}
	if err := ensureSessionCacheDir(pathCacheInstagram); err != nil {
		return err
	}
	return instagramClient.Export(pathCacheInstagram)
}

func instagramLoginNeeds2FA(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "two factor") || strings.Contains(lower, "two_factor")
}

func botLoadAPIs() {
	defer mainWg.Done()
	twitterConnected = false
	instagramConnected = false

	// Twitter API
	if config.Credentials.TwitterAuthEnabled != nil && *config.Credentials.TwitterAuthEnabled {
		{
			twitterScraper = twitterscraper.New()
			if (config.Credentials.TwitterUsername != "" &&
				config.Credentials.TwitterPassword != "") ||
				(config.Credentials.TwitterAuthToken != "" &&
					config.Credentials.TwitterCT0 != "") {
				log.Println(lg("API", "Twitter", color.MagentaString, "Connecting..."))

				// Proxy
				twitterProxy := func(logIt bool) {
					if config.Credentials.TwitterProxy != "" {
						err := twitterScraper.SetProxy(config.Credentials.TwitterProxy)
						if logIt {
							if err != nil {
								log.Println(lg("API", "Twitter", color.HiRedString, "Error setting proxy: %s", err.Error()))
							} else {
								log.Println(lg("API", "Twitter", color.HiMagentaString, "%s", "Proxy set to "+config.Credentials.TwitterProxy))
							}
						}
					}
				}
				twitterProxy(true)

				twitterImport := func() error {
					f, err := os.Open(pathCacheTwitter)
					if err != nil {
						return err
					}
					defer f.Close()
					var cookies []*http.Cookie
					err = json.NewDecoder(f).Decode(&cookies)
					if err != nil {
						return err
					}
					twitterScraper.SetCookies(cookies)
					twitterScraper.IsLoggedIn()
					_, err = twitterScraper.GetProfile("x")
					if err != nil {
						return err
					}
					return nil
				}

				// Login Loop
				twitterLoginCount := 0
			do_twitter_login:
				twitterLoginCount++
				if twitterLoginCount > 1 {
					time.Sleep(3 * time.Second)
				}

				if twitterImport() != nil {
					twitterScraper.ClearCookies()
					var loginerr error
					if config.Credentials.TwitterAuthToken != "" && config.Credentials.TwitterCT0 != "" {
						if config.Debug {
							log.Println(lg("API", "Twitter", color.YellowString,
								"Attempting login Auth Token & CT0..."))
						}
						twitterScraper.SetAuthToken(twitterscraper.AuthToken{
							Token: config.Credentials.TwitterAuthToken, CSRFToken: config.Credentials.TwitterCT0})
					} else if config.Credentials.Twitter2FA != "" {
						if config.Debug {
							log.Println(lg("API", "Twitter", color.YellowString,
								"Attempting login with 2FA/Username/Password..."))
						}
						loginerr = twitterScraper.Login(
							config.Credentials.TwitterUsername, config.Credentials.TwitterPassword,
							config.Credentials.Twitter2FA)
					} else {
						if config.Debug {
							log.Println(lg("API", "Twitter", color.YellowString,
								"Attempting login with Username/Password..."))
						}
						loginerr = twitterScraper.Login(
							config.Credentials.TwitterUsername, config.Credentials.TwitterPassword)
					}

					if loginerr != nil {
						log.Println(lg("API", "Twitter", color.HiRedString, "Login Error: %s", loginerr.Error()))
						if twitterLoginCount <= 3 {
							goto do_twitter_login
						} else {
							log.Println(lg("API", "Twitter", color.HiRedString,
								"Failed to login to Twitter (X), the bot will not fetch this media..."))
						}
					} else {
						if twitterScraper.IsLoggedIn() {
							log.Println(lg("API", "Twitter", color.HiMagentaString, "%s", fmt.Sprintf("Connected to @%s via new login", config.Credentials.TwitterUsername)))
							twitterConnected = true
							if err := exportTwitterSession(); err != nil {
								log.Println(lg("API", "Twitter", color.HiYellowString, "Could not cache session: %s", err))
							}
						} else {
							log.Println(lg("API", "Twitter", color.HiRedString,
								"Scraper login seemed successful but bot is not logged in, Twitter (X) parsing may not work..."))
						}
					}
				} else {
					log.Println(lg("API", "Twitter", color.HiMagentaString,
						"Connected to @%s via cache", config.Credentials.TwitterUsername))
					twitterConnected = true
				}

				if twitterConnected {
					twitterProxy(false)
				}
			} else {
				log.Println(lg("API", "Twitter", color.MagentaString,
					"Twitter (X) login missing, the bot will not fetch this media..."))
			}
		}
	} else {
		log.Println(lg("API", "Twitter", color.RedString,
			"TWITTER AUTHENTICATION IS DISABLED IN SETTINGS..."))
	}

	// Instagram API
	if config.Credentials.InstagramAuthEnabled != nil && *config.Credentials.InstagramAuthEnabled {
		{
			if config.Credentials.InstagramUsername != "" &&
				config.Credentials.InstagramPassword != "" {
				log.Println(lg("API", "Instagram", color.MagentaString, "Connecting..."))

				// Proxy
				instagramProxy := func(logIt bool) {
					if instagramClient != nil && config.Credentials.InstagramProxy != "" {
						insecure := false
						if config.Credentials.InstagramProxyInsecure != nil {
							insecure = *config.Credentials.InstagramProxyInsecure
						}
						forceHTTP2 := false
						if config.Credentials.InstagramProxyForceHTTP2 != nil {
							forceHTTP2 = *config.Credentials.InstagramProxyForceHTTP2
						}
						err := instagramClient.SetProxy(config.Credentials.InstagramProxy, insecure, forceHTTP2)
						if err != nil {
							log.Println(lg("API", "Instagram", color.HiRedString, "Error setting proxy: %s", err.Error()))
						} else {
							log.Println(lg("API", "Instagram", color.HiMagentaString, "%s", "Proxy set to "+config.Credentials.InstagramProxy))
						}
					}
				}

				// Login Loop
				instagramLoginCount := 0
			do_instagram_login:
				instagramLoginCount++
				if instagramLoginCount > 1 {
					time.Sleep(3 * time.Second)
				}
				importedClient, importErr := goinsta.Import(pathCacheInstagram)
				if importErr != nil {
					instagramClient = goinsta.New(config.Credentials.InstagramUsername, config.Credentials.InstagramPassword)
					instagramProxy(true)
					if err := instagramClient.Login(); err != nil {
						// 2fa
						if instagramLoginNeeds2FA(err) {
							// Generate TOTP
							totp := ""
							if config.Credentials.InstagramTOTP != nil {
								totp = *config.Credentials.InstagramTOTP
							}
							if totp != "" {
								instagramClient.SetTOTPSeed(totp)
								if err := instagramClient.TwoFactorInfo.Login2FA(); err != nil {
									log.Println(lg("API", "Instagram", color.HiRedString, "2FA-Generated Login Error: %s", err.Error()))
									if instagramLoginCount <= 3 {
										goto do_instagram_login
									} else {
										log.Println(lg("API", "Instagram", color.HiRedString,
											"Failed to login to Instagram, the bot will not fetch this media..."))
									}
								} else {
									log.Println(lg("API", "Instagram", color.HiMagentaString,
										"Connected to @%s via new 2FA-Generated login", instagramClient.Account.Username))
									instagramConnected = true
									if err := exportInstagramSession(); err != nil {
										log.Println(lg("API", "Instagram", color.HiYellowString, "Could not cache session: %s", err))
									}
								}
							} else { // Manual TOTP
								log.Println(lg("API", "Instagram", color.HiYellowString,
									"MANUAL 2FA LOGIN\nPlease enter OTP code... "))
								reader := bufio.NewReader(os.Stdin)
								inputCode, _ := reader.ReadString('\n')
								inputCode = strings.ReplaceAll(inputCode, "\n", "")
								inputCode = strings.ReplaceAll(inputCode, "\r", "")
								if inputCode != "" {
									if err := instagramClient.TwoFactorInfo.Login2FA(inputCode); err != nil {
										log.Println(lg("API", "Instagram", color.HiRedString, "2FA-Code Login Error: %s", err.Error()))
										if instagramLoginCount <= 3 {
											goto do_instagram_login
										} else {
											log.Println(lg("API", "Instagram", color.HiRedString,
												"Failed to login to Instagram, the bot will not fetch this media..."))
										}
									} else {
										log.Println(lg("API", "Instagram", color.HiMagentaString,
											"Connected to @%s via new 2FA-Code login", instagramClient.Account.Username))
										instagramConnected = true
										if err := exportInstagramSession(); err != nil {
											log.Println(lg("API", "Instagram", color.HiYellowString, "Could not cache session: %s", err))
										}
									}
								} else {
									log.Println(lg("API", "Instagram", color.HiRedString,
										"Blank OTP given... Try again."))
								}
							}
						} else { // non-2fa
							log.Println(lg("API", "Instagram", color.HiRedString, "Login Error: %s", err.Error()))
							if instagramLoginCount <= 3 {
								goto do_instagram_login
							} else {
								log.Println(lg("API", "Instagram", color.HiRedString,
									"Failed to login to Instagram, the bot will not fetch this media..."))
							}
						}
					} else {
						log.Println(lg("API", "Instagram", color.HiMagentaString,
							"Connected to @%s via new login", instagramClient.Account.Username))
						instagramConnected = true
						if err := exportInstagramSession(); err != nil {
							log.Println(lg("API", "Instagram", color.HiYellowString, "Could not cache session: %s", err))
						}
					}
				} else {
					instagramClient = importedClient
					instagramProxy(true)
					log.Println(lg("API", "Instagram", color.HiMagentaString,
						"Connected to @%s via cache", instagramClient.Account.Username))
					instagramConnected = true
				}
			} else {
				log.Println(lg("API", "Instagram", color.MagentaString,
					"Instagram login missing, the bot will not fetch this media..."))
			}
		}
	} else {
		log.Println(lg("API", "Instagram", color.RedString,
			"INSTAGRAM AUTHENTICATION IS DISABLED IN SETTINGS..."))
	}

}

//#region Twitter

func getTwitterUrls(inputURL string) (map[string]string, error) {
	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse Twitter media URL: %w", err)
	}
	host := strings.ToLower(parsedURL.Hostname())
	if host != "pbs.twimg.com" && !(strings.HasPrefix(host, "pbs-") && strings.HasSuffix(host, ".twimg.com")) {
		return nil, errors.New("URL is not a Twitter media URL")
	}

	parsedURL.Fragment = ""
	query := parsedURL.Query()
	if query.Get("format") != "" {
		query.Set("name", "orig")
		parsedURL.RawQuery = query.Encode()
	} else {
		// Older Twitter media URLs use a :large/:orig suffix instead of query parameters.
		lastSlash := strings.LastIndex(parsedURL.Path, "/")
		if colon := strings.LastIndex(parsedURL.Path, ":"); colon > lastSlash {
			parsedURL.Path = parsedURL.Path[:colon]
		}
		parsedURL.Path += ":orig"
	}

	mediaURL := parsedURL.String()
	return map[string]string{mediaURL: filenameFromURL(mediaURL)}, nil
}

func getTwitterStatusUrls(inputURL string, m *discordgo.Message) (map[string]string, error) {
	if strings.Contains(inputURL, "/photo/") {
		inputURL = inputURL[:strings.Index(inputURL, "/photo/")]
	}
	if strings.Contains(inputURL, "/video/") {
		inputURL = inputURL[:strings.Index(inputURL, "/video/")]
	}

	matches := regexUrlTwitterStatus.FindStringSubmatch(inputURL)
	if len(matches) < 5 {
		return nil, errors.New("unable to parse Twitter status URL")
	}
	if _, err := strconv.ParseInt(matches[4], 10, 64); err != nil {
		return nil, err
	}
	if twitterScraper == nil {
		return nil, errors.New("Twitter scraper is not initialized")
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		tweet, err := twitterScraper.GetTweet(matches[4])
		if err != nil {
			lastErr = err
		} else if tweet != nil {
			links := make(map[string]string)
			for _, photo := range tweet.Photos {
				if photo.URL == "" {
					continue
				}
				for foundURL, filename := range getParsedLinks(photo.URL, m) {
					links[foundURL] = filename
				}
			}
			for _, video := range tweet.Videos {
				if video.URL == "" {
					continue
				}
				for foundURL, filename := range getParsedLinks(video.URL, m) {
					links[foundURL] = filename
				}
			}
			for _, gif := range tweet.GIFs {
				if gif.URL == "" {
					continue
				}
				for foundURL, filename := range getParsedLinks(gif.URL, m) {
					links[foundURL] = filename
				}
			}
			if len(links) > 0 {
				return links, nil
			}
			lastErr = errors.New("Twitter status contains no downloadable media")
		}

		if attempt < 2 {
			if config.Debug {
				log.Println(lg("API", "Twitter", color.HiRedString, "No content found in post, retrying %s...", matches[4]))
			}
			time.Sleep(1 * time.Second)
		}
	}
	return nil, lastErr
}

//#endregion

//#region Instagram

func getInstagramUrls(inputURL string, m *discordgo.Message) (map[string]string, error) {
	if instagramClient == nil {
		return nil, errors.New("invalid Instagram API credentials")
	}

	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse Instagram URL: %w", err)
	}
	segments := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	shortcode := ""
	for index, segment := range segments {
		if (segment == "p" || segment == "reel" || segment == "tv") && index+1 < len(segments) {
			shortcode = segments[index+1]
			break
		}
	}
	if shortcode == "" {
		return nil, errors.New("unable to find Instagram shortcode")
	}

	mediaID, err := goinsta.MediaIDFromShortID(shortcode)
	if err != nil {
		return nil, err
	}
	media, err := instagramClient.GetMedia(mediaID)
	if err != nil {
		return nil, err
	}
	if len(media.Items) == 0 {
		return nil, errors.New("Instagram post contains no media items")
	}

	item := media.Items[0]
	username := item.User.Username
	if username == "" {
		username = "instagram"
	}
	links := make(map[string]string)
	addVideo := func(videoURL, filename string) {
		if videoURL != "" {
			links[videoURL] = filename
		}
	}
	addPhoto := func(photoURL, filename string) {
		if photoURL != "" {
			links[photoURL] = filename
		}
	}

	postType := item.MediaToString()
	if postType == "carousel" {
		for index, carouselItem := range item.CarouselMedia {
			filename := fmt.Sprintf("%s %d %s", shortcode, index, username)
			if carouselItem.MediaToString() == "video" {
				if len(carouselItem.Videos) > 0 {
					addVideo(carouselItem.Videos[0].URL, filename)
				}
			} else {
				addPhoto(carouselItem.Images.GetBest(), filename)
			}
		}
	} else if postType == "video" {
		if len(item.Videos) > 0 {
			addVideo(item.Videos[0].URL, fmt.Sprintf("%s %s", shortcode, username))
		}
	} else {
		addPhoto(item.Images.GetBest(), fmt.Sprintf("%s %s", shortcode, username))
	}
	if len(links) == 0 {
		return nil, errors.New("Instagram post contains no downloadable media")
	}
	return links, nil
}

//#endregion

//#region Imgur

func getImgurSingleUrls(url string) (map[string]string, error) {
	url = regexp.MustCompile(`(r\/[^\/]+\/)`).ReplaceAllString(url, "") // remove subreddit url
	url = strings.Replace(url, "imgur.com/", "imgur.com/download/", -1)
	url = strings.Replace(url, ".gifv", "", -1)
	return map[string]string{url: ""}, nil
}

type imgurAlbumObject struct {
	Data []struct {
		Link string
	}
}

func getImgurAlbumUrls(url string) (map[string]string, error) {
	url = regexp.MustCompile(`(#[A-Za-z0-9]+)?$`).ReplaceAllString(url, "") // remove anchor
	afterLastSlash := strings.LastIndex(url, "/")
	albumId := url[afterLastSlash+1:]
	headers := make(map[string]string)
	headers["Authorization"] = "Client-ID " + imgurClientID
	imgurAlbumObject := new(imgurAlbumObject)
	if err := getJSONwithHeaders("https://api.imgur.com/3/album/"+albumId+"/images", imgurAlbumObject, headers); err != nil {
		return getImgurSingleUrls(url)
	}
	links := make(map[string]string)
	for _, v := range imgurAlbumObject.Data {
		links[v.Link] = ""
	}
	if len(links) <= 0 {
		return getImgurSingleUrls(url)
	}
	log.Printf("Found imgur album with %d images (url: %s)\n", len(links), url)
	return links, nil
}

//#endregion

//#region Streamable

type streamableObject struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Files  struct {
		Mp4 struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"mp4"`
		Mp4Mobile struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"mp4-mobile"`
	} `json:"files"`
	URL          string      `json:"url"`
	ThumbnailURL string      `json:"thumbnail_url"`
	Message      interface{} `json:"message"`
}

func getStreamableUrls(url string) (map[string]string, error) {
	matches := regexUrlStreamable.FindStringSubmatch(url)
	if len(matches) < 4 {
		return nil, errors.New("unable to parse Streamable URL")
	}
	shortcode := matches[3]
	if shortcode == "" {
		return nil, errors.New("unable to get shortcode from URL")
	}
	reqUrl := fmt.Sprintf("https://api.streamable.com/videos/%s", shortcode)
	streamable := new(streamableObject)
	if err := getJSON(reqUrl, streamable); err != nil {
		return nil, err
	}
	if streamable.Status != 2 || streamable.Files.Mp4.URL == "" {
		return nil, errors.New("streamable object has no download candidate")
	}
	link := streamable.Files.Mp4.URL
	if !strings.HasPrefix(link, "http") {
		link = "https:" + link
	}
	links := make(map[string]string)
	links[link] = ""
	return links, nil
}

//#endregion

//#region Gfycat

type gfycatObject struct {
	GfyItem struct {
		Mp4URL string `json:"mp4Url"`
	} `json:"gfyItem"`
}

func getGfycatUrls(url string) (map[string]string, error) {
	parts := strings.Split(url, "/")
	if len(parts) < 3 {
		return nil, errors.New("unable to parse Gfycat URL")
	}
	gfycatId := strings.TrimSuffix(parts[len(parts)-1], ".html")
	gfycatObject := new(gfycatObject)
	if err := getJSON("https://api.gfycat.com/v1/gfycats/"+gfycatId, gfycatObject); err != nil {
		return nil, err
	}
	gfycatUrl := gfycatObject.GfyItem.Mp4URL
	if gfycatUrl == "" {
		return nil, errors.New("failed to read response from Gfycat")
	}
	return map[string]string{gfycatUrl: ""}, nil
}

//#endregion

//#region Flickr

type flickrPhotoSizeObject struct {
	Label  string `json:"label"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Source string `json:"source"`
	URL    string `json:"url"`
	Media  string `json:"media"`
}

type flickrPhotoObject struct {
	Sizes struct {
		Canblog     int                     `json:"canblog"`
		Canprint    int                     `json:"canprint"`
		Candownload int                     `json:"candownload"`
		Size        []flickrPhotoSizeObject `json:"size"`
	} `json:"sizes"`
	Stat string `json:"stat"`
}

func getFlickrUrlFromPhotoId(photoId string) string {
	reqUrl := fmt.Sprintf("https://www.flickr.com/services/rest/?format=json&nojsoncallback=1&method=%s&api_key=%s&photo_id=%s",
		"flickr.photos.getSizes", config.Credentials.FlickrApiKey, photoId)
	flickrPhoto := new(flickrPhotoObject)
	if err := getJSON(reqUrl, flickrPhoto); err != nil {
		return ""
	}
	var bestSize flickrPhotoSizeObject
	for _, size := range flickrPhoto.Sizes.Size {
		if bestSize.Label == "" {
			bestSize = size
		} else {
			if size.Width > bestSize.Width || size.Height > bestSize.Height {
				bestSize = size
			}
		}
	}
	return bestSize.Source
}

func getFlickrPhotoUrls(url string) (map[string]string, error) {
	if config.Credentials.FlickrApiKey == "" {
		return nil, errors.New("invalid Flickr API Key Set")
	}
	matches := regexUrlFlickrPhoto.FindStringSubmatch(url)
	if len(matches) < 6 {
		return nil, errors.New("unable to parse Flickr photo URL")
	}
	photoId := matches[5]
	if photoId == "" {
		return nil, errors.New("unable to get Photo ID from URL")
	}
	return map[string]string{getFlickrUrlFromPhotoId(photoId): ""}, nil
}

type flickrAlbumObject struct {
	Photoset struct {
		ID        string `json:"id"`
		Primary   string `json:"primary"`
		Owner     string `json:"owner"`
		Ownername string `json:"ownername"`
		Photo     []struct {
			ID        string `json:"id"`
			Secret    string `json:"secret"`
			Server    string `json:"server"`
			Farm      int    `json:"farm"`
			Title     string `json:"title"`
			Isprimary string `json:"isprimary"`
			Ispublic  int    `json:"ispublic"`
			Isfriend  int    `json:"isfriend"`
			Isfamily  int    `json:"isfamily"`
		} `json:"photo"`
		Page    int    `json:"page"`
		PerPage int    `json:"per_page"`
		Perpage int    `json:"perpage"`
		Pages   int    `json:"pages"`
		Total   string `json:"total"`
		Title   string `json:"title"`
	} `json:"photoset"`
	Stat string `json:"stat"`
}

func getFlickrAlbumUrls(url string) (map[string]string, error) {
	if config.Credentials.FlickrApiKey == "" {
		return nil, errors.New("invalid Flickr API Key Set")
	}
	matches := regexUrlFlickrAlbum.FindStringSubmatch(url)
	if len(matches) < 10 || matches[9] == "" {
		return nil, errors.New("unable to find Flickr Album ID in URL")
	}
	albumId := matches[9]
	if albumId == "" {
		return nil, errors.New("unable to get Album ID from URL")
	}
	reqUrl := fmt.Sprintf("https://www.flickr.com/services/rest/?format=json&nojsoncallback=1&method=%s&api_key=%s&photoset_id=%s&per_page=500",
		"flickr.photosets.getPhotos", config.Credentials.FlickrApiKey, albumId)
	flickrAlbum := new(flickrAlbumObject)
	if err := getJSON(reqUrl, flickrAlbum); err != nil {
		return nil, err
	}
	links := make(map[string]string)
	for _, photo := range flickrAlbum.Photoset.Photo {
		links[getFlickrUrlFromPhotoId(photo.ID)] = ""
	}
	return links, nil
}

func getFlickrAlbumShortUrls(url string) (map[string]string, error) {
	result, err := jsonHTTPClient.Get(url)
	if err != nil {
		return nil, errors.New("Error getting long URL from shortened Flickr Album URL: " + err.Error())
	}
	defer result.Body.Close()
	if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("short Flickr Album URL returned HTTP status %s", result.Status)
	}
	if result.Request == nil || result.Request.URL == nil {
		return nil, errors.New("short Flickr Album URL did not redirect to a valid URL")
	}
	if regexUrlFlickrAlbum.MatchString(result.Request.URL.String()) {
		return getFlickrAlbumUrls(result.Request.URL.String())
	}
	return nil, errors.New("encountered invalid URL while trying to get long URL from short Flickr Album URL")
}

//#endregion

//#region Tistory

// getTistoryUrls downloads tistory URLs
// http://t1.daumcdn.net/cfile/tistory/[…] => http://t1.daumcdn.net/cfile/tistory/[…]
// http://t1.daumcdn.net/cfile/tistory/[…]?original => as is
func getTistoryUrls(link string) (map[string]string, error) {
	parsedURL, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("unable to parse Tistory URL: %w", err)
	}
	if parsedURL.Scheme == "" || parsedURL.Hostname() == "" {
		return nil, errors.New("unable to parse Tistory URL")
	}
	if _, exists := parsedURL.Query()["original"]; !exists {
		if parsedURL.RawQuery == "" {
			parsedURL.RawQuery = "original"
		} else {
			parsedURL.RawQuery += "&original"
		}
	}
	return map[string]string{parsedURL.String(): ""}, nil
}

func getLegacyTistoryUrls(link string) (map[string]string, error) {
	link = strings.Replace(link, "/image/", "/original/", -1)
	return map[string]string{link: ""}, nil
}

func getTistoryWithCDNUrls(urlI string) (map[string]string, error) {
	parsedURL, err := url.Parse(urlI)
	if err != nil {
		return nil, err
	}
	parameters := parsedURL.Query()
	if val, ok := parameters["fname"]; ok {
		if len(val) > 0 {
			if regexUrlTistoryLegacy.MatchString(val[0]) {
				return getLegacyTistoryUrls(val[0])
			}
		}
	}
	return nil, nil
}

func getPossibleTistorySiteUrls(url string) (map[string]string, error) {
	client := jsonHTTPClient
	request, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Add("Accept-Encoding", "identity")
	request.Header.Add("User-Agent", sneakyUserAgent)
	respHead, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer respHead.Body.Close()
	if respHead.StatusCode < http.StatusOK || respHead.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Tistory page returned HTTP status %s", respHead.Status)
	}

	contentType := ""
	for headerKey, headerValue := range respHead.Header {
		if strings.EqualFold(headerKey, "Content-Type") && len(headerValue) > 0 {
			contentType = headerValue[0]
		}
	}
	if !strings.Contains(contentType, "text/html") {
		return nil, nil
	}

	request, err = http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Add("Accept-Encoding", "identity")
	request.Header.Add("User-Agent", sneakyUserAgent)
	resp, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Tistory page returned HTTP status %s", resp.Status)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var links = make(map[string]string)

	doc.Find(".article img, #content img, div[role=main] img, .section_blogview img").Each(func(i int, s *goquery.Selection) {
		foundUrl, exists := s.Attr("src")
		if exists {
			if regexUrlTistoryLegacyWithCDN.MatchString(foundUrl) {
				finalTistoryUrls, _ := getTistoryWithCDNUrls(foundUrl)
				if len(finalTistoryUrls) > 0 {
					for finalTistoryUrl := range finalTistoryUrls {
						foundFilename := s.AttrOr("filename", "")
						links[finalTistoryUrl] = foundFilename
					}
				}
			} else if regexUrlTistoryLegacy.MatchString(foundUrl) {
				finalTistoryUrls, _ := getLegacyTistoryUrls(foundUrl)
				if len(finalTistoryUrls) > 0 {
					for finalTistoryUrl := range finalTistoryUrls {
						foundFilename := s.AttrOr("filename", "")
						links[finalTistoryUrl] = foundFilename
					}
				}
			}
		}
	})

	if len(links) > 0 {
		log.Printf("[%s] Found tistory album with %d images (url: %s)\n", time.Now().Format(time.Stamp), len(links), url)
	}
	return links, nil
}

//#endregion

//#region Reddit

// This is very crude but works for now
type redditThreadObject []struct {
	Kind string `json:"kind"`
	Data struct {
		Children []struct {
			Data struct {
				URL       string `json:"url_overridden_by_dest"`
				Subreddit string `json:"subreddit"`
				ID        string `json:"id"`
			} `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

func getRedditPostUrls(link string) (map[string]string, error) {
	if strings.Contains(link, "?") {
		link = link[:strings.Index(link, "?")]
	}
	redditThread := new(redditThreadObject)
	headers := make(map[string]string)
	headers["Accept-Encoding"] = "identity"
	headers["User-Agent"] = sneakyUserAgent
	err := getJSONwithHeaders(link+".json", redditThread, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to parse json from reddit post:\t%s", err)
	}

	if len(*redditThread) == 0 || len((*redditThread)[0].Data.Children) == 0 {
		return nil, errors.New("Reddit response contains no posts")
	}
	post := (*redditThread)[0].Data.Children[0].Data
	if post.URL != "" {
		filename := fmt.Sprintf("Reddit-%s_%s %s", post.Subreddit, post.ID, filenameFromURL(post.URL))
		return map[string]string{post.URL: filename}, nil
	}
	return nil, nil
}

//#endregion
