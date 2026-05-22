package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SharkBot-Dev/DiscordMusicBot/lib"
	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgolink/v4/disgolink"
	"github.com/disgoorg/disgolink/v4/lavalink"
	"github.com/disgoorg/snowflake/v2"
	"github.com/joho/godotenv"
)

type LoopMode int

const (
	LoopModeNone LoopMode = iota
	LoopModeSingle
	LoopModeQueue
)

func (l LoopMode) String() string {
	switch l {
	case LoopModeSingle:
		return "1曲ループ"
	case LoopModeQueue:
		return "全曲ループ"
	default:
		return "オフ"
	}
}

type GuildQueue struct {
	Tracks []lavalink.Track
	Loop   LoopMode
	Mu     sync.Mutex
}

var (
	session        *discordgo.Session
	lavalinkClient *disgolink.Client
	commands       = []*discordgo.ApplicationCommand{
		{
			Name:        "help",
			Description: "Botの使い方を知ります",
		},
		{
			Name:        "about",
			Description: "Botの情報を取得します",
		},
		{
			Name:        "play",
			Description: "音楽を再生またはキューに追加します",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "query",
					Description: "検索ワード&音楽Urlを入力",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    false,
				},
				{
					Name:        "file",
					Description: "ファイルをアップロード",
					Type:        discordgo.ApplicationCommandOptionAttachment,
					Required:    false,
				},
			},
		},
		{
			Name:        "stop",
			Description: "音楽を停止し、キューをクリアしてVCからBotを退出させします",
		},
		{
			Name:        "queue",
			Description: "現在の再生待ちリストを表示します",
		},
		{
			Name:        "skip",
			Description: "現在再生中の曲をスキップします",
		},
		{
			Name:        "loop",
			Description: "ループモードを切り替えます",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "mode",
					Description: "ループモードを選択",
					Type:        discordgo.ApplicationCommandOptionString,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{
							Name:  "オフ",
							Value: "off",
						},
						{
							Name:  "一曲ループ",
							Value: "single",
						},
						{
							Name:  "キューループ",
							Value: "queue",
						},
					},
					Required: true,
				},
			},
		},
		{
			Name:        "now",
			Description: "現在再生している音楽を取得します",
		},
		{
			Name:        "volume",
			Description: "音楽のボリュームを指定します",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "volume",
					Description: "音楽のボリュームを入力",
					Type:        discordgo.ApplicationCommandOptionInteger,
					Required:    true,
				},
			},
		},
	}
	startTime time.Time

	queues  = make(map[string]*GuildQueue)
	queueMu sync.Mutex
)

func getGuildQueue(guildID string) *GuildQueue {
	queueMu.Lock()
	defer queueMu.Unlock()
	if q, exists := queues[guildID]; exists {
		return q
	}
	q := &GuildQueue{Tracks: make([]lavalink.Track, 0)}
	queues[guildID] = q
	return q
}

func stringToSnowFlake(s string) snowflake.ID {
	u, _ := strconv.ParseUint(s, 10, 64)
	return snowflake.ID(u)
}

func stringToSnowFlakePointer(s string) *snowflake.ID {
	u, _ := strconv.ParseUint(s, 10, 64)
	id := snowflake.ID(u)
	return &id
}

func stringToPointer(s string) *string {
	sp := s
	return &sp
}

func ParseQuery(input string) string {
	trimmed := strings.TrimSpace(input)
	u, err := url.ParseRequestURI(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "scsearch:" + input
	}

	return input
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: Error loading .env file (using system environment variables)")
	}

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("環境変数 DISCORD_TOKEN が設定されていません")
	}

	clientIdStr := os.Getenv("DISCORD_CLIENT_ID")
	if clientIdStr == "" {
		log.Fatal("環境変数 DISCORD_CLIENT_ID が設定されていません")
	}

	LAVALINK_ADDRESS := os.Getenv("LAVALINK_ADDRESS")
	if LAVALINK_ADDRESS == "" {
		log.Fatal("環境変数 LAVALINK_ADDRESS が設定されていません")
	}

	LAVALINK_PORT := os.Getenv("LAVALINK_PORT")
	if LAVALINK_PORT == "" {
		log.Fatal("環境変数 LAVALINK_PORT が設定されていません")
	}

	LAVALINK_PASSWORD := os.Getenv("LAVALINK_PASSWORD")
	if LAVALINK_PASSWORD == "" {
		log.Fatal("環境変数 LAVALINK_PASSWORD が設定されていません")
	}

	parsedClientId, err := strconv.ParseUint(clientIdStr, 10, 64)
	if err != nil {
		log.Fatal("DISCORD_CLIENT_ID のパースに失敗しました: ", err)
	}

	sessionManager := &lib.DiscordSessionManager{}

	lavalinkClient = disgolink.New(snowflake.ID(parsedClientId), disgolink.WithListenerFunc(onTrackEnd))

	_, connectErr := lavalinkClient.AddNode(context.TODO(), disgolink.NodeConfig{
		Name:     "main",
		Address:  LAVALINK_ADDRESS + ":" + LAVALINK_PORT,
		Password: LAVALINK_PASSWORD,
		Secure:   false,
	})

	if connectErr != nil {
		log.Fatal("Lavalinkに接続できませんでした: ", connectErr)
	}

	session = sessionManager.InitializeSession(token)

	session.Identify.Intents |= discordgo.IntentsGuilds
	session.Identify.Intents |= discordgo.IntentsGuildVoiceStates

	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Print("起動しました。")

		go func() {
			_, err := s.ApplicationCommandBulkOverwrite(s.State.Application.ID, "", commands)
			if err != nil {
				log.Println("コマンドの同期に失敗しました: ", err)
			} else {
				log.Print("スラッシュコマンドを同期しました。")
			}
		}()

		go func() {
			for {
				s.UpdateCustomStatus("🎵/playで再生")
				time.Sleep(10 * time.Second)
			}
		}()
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.VoiceStateUpdate) {
		lavalinkClient.OnVoiceStateUpdate(context.TODO(), stringToSnowFlake(i.VoiceState.GuildID), stringToSnowFlakePointer(i.VoiceState.ChannelID), i.VoiceState.SessionID)

		checkVoiceChannelEmpty(s, i.VoiceState.GuildID)
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.VoiceServerUpdate) {
		lavalinkClient.OnVoiceServerUpdate(context.TODO(), stringToSnowFlake(i.GuildID), i.Token, i.Endpoint)
	})

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}

		commandName := i.ApplicationCommandData().Name
		switch commandName {
		case "help":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Embeds: []*discordgo.MessageEmbed{
						{
							Title: "音楽Botの使い方",
							Color: 3447003,
							Fields: []*discordgo.MessageEmbedField{
								{Name: "/play [検索ワードまたはURL]", Value: "音楽を再生、または再生待ちリストに追加します。", Inline: false},
								{Name: "/skip", Value: "現在再生中の曲をスキップします。", Inline: false},
								{Name: "/loop [ループモード]", Value: "ループモードを切り替えます。", Inline: false},
								{Name: "/queue", Value: "現在の再生待ちリストを表示します。", Inline: false},
								{Name: "/stop", Value: "音楽の再生を停止し、Botを退出させます。", Inline: false},
								{Name: "/now", Value: "現在再生している音楽を取得します", Inline: false},
								{Name: "/volume [ボリューム]", Value: "音楽のボリュームを指定します", Inline: false},
								{Name: "/help", Value: "Botの使い方を知ります", Inline: false},
								{Name: "/about", Value: "Botの情報を表示します。", Inline: false},
							},
						},
					},
					Flags: discordgo.MessageFlagsEphemeral,
				},
			})
		case "about":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags: discordgo.MessageFlagsEphemeral,
				},
			})

			uptime := time.Since(startTime)
			uptimeStr := fmt.Sprintf("%d時間%d分", int(uptime.Hours()), int(uptime.Minutes())%60)
			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Embeds: &[]*discordgo.MessageEmbed{
					{
						Title: "音楽Botの情報",
						Color: 3447003,
						Fields: []*discordgo.MessageEmbedField{
							{
								Name:   "サーバー数",
								Value:  strconv.Itoa(len(s.State.Guilds)) + " サーバー",
								Inline: false,
							},
							{
								Name:   "起動時間",
								Value:  uptimeStr,
								Inline: false,
							},
						},
					},
				},
			})
		case "play":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			var vs *discordgo.VoiceState
			for _, guild := range s.State.Guilds {
				if guild.ID == i.GuildID {
					for _, state := range guild.VoiceStates {
						if state.UserID == i.Member.User.ID {
							vs = state
							break
						}
					}
				}
			}

			if vs == nil || vs.ChannelID == "" {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 先にボイスチャンネルに参加してください。"),
				})
				return
			}

			options := i.ApplicationCommandData().Options
			optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption)
			for _, opt := range options {
				optionMap[opt.Name] = opt
			}

			var query string

			if fileOpt, exists := optionMap["file"]; exists {
				attachmentID := fileOpt.Value.(string)
				if attachment, ok := i.ApplicationCommandData().Resolved.Attachments[attachmentID]; ok {
					query = attachment.URL
				}
			}

			if query == "" {
				if queryOpt, exists := optionMap["query"]; exists {
					query = ParseQuery(queryOpt.StringValue())
				}
			}

			if query == "" {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 検索ワード、URL、またはファイルのいずれかを指定してください。"),
				})
				return
			}

			err := s.ChannelVoiceJoinManual(i.GuildID, vs.ChannelID, false, false)
			if err != nil {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ ボイスチャンネルへの参加に失敗しました。"),
				})
				return
			}

			player := lavalinkClient.Player(stringToSnowFlake(i.GuildID))
			bestNode := lavalinkClient.BestNode()
			if bestNode == nil {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 利用可能なLavalinkノードがありません。"),
				})
				return
			}

			handleTrackEnqueue := func(track lavalink.Track) {
				gQueue := getGuildQueue(i.GuildID)
				gQueue.Mu.Lock()

				if player.Track == nil {
					gQueue.Mu.Unlock()
					player.Update(context.TODO(), disgolink.WithTrack(track))
					s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: stringToPointer("🎵 再生中: " + track.Info.Title),
					})
				} else {
					gQueue.Tracks = append(gQueue.Tracks, track)
					gQueue.Mu.Unlock()
					s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: stringToPointer("⏳ キューに追加されました: " + track.Info.Title),
					})
				}
			}

			bestNode.Rest.LoadTracksHandler(context.TODO(), query, disgolink.NewTrackLoadingResultHandler(
				func(track lavalink.Track) {
					handleTrackEnqueue(track)
				},
				func(playlist lavalink.Playlist) {
					for _, track := range playlist.Tracks {
						gQueue := getGuildQueue(i.GuildID)
						gQueue.Mu.Lock()
						if player.Track == nil {
							gQueue.Mu.Unlock()
							player.Update(context.TODO(), disgolink.WithTrack(track))
						} else {
							gQueue.Tracks = append(gQueue.Tracks, track)
							gQueue.Mu.Unlock()
						}
					}
					s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: stringToPointer(fmt.Sprintf("🎵 プレイリストから %d 曲を読み込みました", len(playlist.Tracks))),
					})
				},
				func(tracks []lavalink.Track) {
					if len(tracks) > 0 {
						handleTrackEnqueue(tracks[0])
					}
				},
				func() {
					s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: stringToPointer("🤔 検索結果が見つかりませんでした。"),
					})
				},
				func(err error) {
					log.Print("エラー: ", err)
					s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: stringToPointer("❌ 予期しないエラーが発生しました。"),
					})
				},
			))
		case "stop":
			var vs *discordgo.VoiceState
			for _, guild := range s.State.Guilds {
				if guild.ID == i.GuildID {
					for _, state := range guild.VoiceStates {
						if state.UserID == i.Member.User.ID {
							vs = state
							break
						}
					}
				}
			}

			if vs == nil || vs.ChannelID == "" {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 先にボイスチャンネルに参加してください。"),
				})
				return
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "⏹️音楽を停止し、キューをクリアしました。",
				},
			})

			stopAndDisconnect(s, i.GuildID)

		case "queue":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			gQueue := getGuildQueue(i.GuildID)
			gQueue.Mu.Lock()
			defer gQueue.Mu.Unlock()

			player := lavalinkClient.Player(stringToSnowFlake(i.GuildID))
			currentTrack := player.Track

			if currentTrack == nil && len(gQueue.Tracks) == 0 {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ キューは空っぽです。"),
				})
				return
			}

			msg := fmt.Sprintf("📊 現在の再生リスト (ループ: %s)\n", gQueue.Loop.String())
			if currentTrack != nil {
				msg += fmt.Sprintf("▶️ 再生中: %s\n\n", currentTrack.Info.Title)
			}

			if len(gQueue.Tracks) > 0 {
				msg += "⏳ 再生待ち:\n"
				for i, track := range gQueue.Tracks {
					if i >= 10 {
						msg += fmt.Sprintf("...他 %d 曲\n", len(gQueue.Tracks)-10)
						break
					}
					msg += fmt.Sprintf("%d. %s\n", i+1, track.Info.Title)
				}
			} else {
				msg += "再生待ちの曲はありません。"
			}

			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &msg,
			})
		case "skip":
			var vs *discordgo.VoiceState
			for _, guild := range s.State.Guilds {
				if guild.ID == i.GuildID {
					for _, state := range guild.VoiceStates {
						if state.UserID == i.Member.User.ID {
							vs = state
							break
						}
					}
				}
			}

			if vs == nil || vs.ChannelID == "" {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 先にボイスチャンネルに参加してください。"),
				})
				return
			}

			player := lavalinkClient.Player(stringToSnowFlake(i.GuildID))
			if player == nil || player.Track == nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "❌ 現在何も再生されていません。",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			player.Update(context.TODO(), disgolink.WithNullTrack())

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "⏭️ 曲をスキップしました。",
				},
			})
		case "loop":
			var vs *discordgo.VoiceState
			for _, guild := range s.State.Guilds {
				if guild.ID == i.GuildID {
					for _, state := range guild.VoiceStates {
						if state.UserID == i.Member.User.ID {
							vs = state
							break
						}
					}
				}
			}

			if vs == nil || vs.ChannelID == "" {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 先にボイスチャンネルに参加してください。"),
				})
				return
			}

			gQueue := getGuildQueue(i.GuildID)

			mode := i.ApplicationCommandData().GetOption("mode").StringValue()

			gQueue.Mu.Lock()
			var currentLoop = gQueue.Loop
			switch mode {
			case "off":
				gQueue.Loop = LoopModeNone
				currentLoop = gQueue.Loop
			case "single":
				gQueue.Loop = LoopModeSingle
				currentLoop = gQueue.Loop
			case "queue":
				gQueue.Loop = LoopModeQueue
				currentLoop = gQueue.Loop
			}
			gQueue.Mu.Unlock()

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf("🔁 ループモードを %s に変更しました。", currentLoop.String()),
				},
			})
		case "now":
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			player := lavalinkClient.Player(stringToSnowFlake(i.GuildID))
			if player == nil || player.Track == nil {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 現在は何も再生していません。"),
				})
				return
			}

			currentTrack := player.Track

			embed := &discordgo.MessageEmbed{
				Title:       currentTrack.Info.Title,
				Description: currentTrack.Info.Author,
				Color:       3447003,
			}

			if currentTrack.Info.ArtworkURL != nil && *currentTrack.Info.ArtworkURL != "" {
				embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
					URL: *currentTrack.Info.ArtworkURL,
				}
			}

			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Embeds: &[]*discordgo.MessageEmbed{
					embed,
				},
				Content: stringToPointer("🎵 再生中の曲を取得しました👇"),
			})
		case "volume":
			var vs *discordgo.VoiceState
			for _, guild := range s.State.Guilds {
				if guild.ID == i.GuildID {
					for _, state := range guild.VoiceStates {
						if state.UserID == i.Member.User.ID {
							vs = state
							break
						}
					}
				}
			}

			if vs == nil || vs.ChannelID == "" {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ 先にボイスチャンネルに参加してください。"),
				})
				return
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			})

			player := lavalinkClient.Player(stringToSnowFlake(i.GuildID))

			volume := i.ApplicationCommandData().GetOption("volume").IntValue()
			intVolume := int(volume)
			if intVolume >= 151 {
				s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: stringToPointer("❌ ボリュームは150%まで指定できます"),
				})
				return
			}

			player.Update(context.TODO(), disgolink.WithVolume(intVolume), disgolink.WithTrack(*player.Track), disgolink.WithPosition(player.State.Position))
			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: stringToPointer(fmt.Sprintf("🔊 音量を %s％ に設定しました。", strconv.Itoa(intVolume))),
			})
		}
	})

	if err := session.Open(); err != nil {
		log.Fatalf("Discordセッションのオープンに失敗: %v", err)
	}

	startTime = time.Now()
	defer session.Close()

	log.Println("ボットが起動しました。Ctrl+Cで終了します。")
	waitForExitSignal()
}

func stopAndDisconnect(s *discordgo.Session, guildID string) {
	gQueue := getGuildQueue(guildID)
	gQueue.Mu.Lock()
	gQueue.Tracks = make([]lavalink.Track, 0)
	gQueue.Loop = LoopModeNone
	gQueue.Mu.Unlock()

	player := lavalinkClient.Player(stringToSnowFlake(guildID))
	if player != nil {
		player.Update(context.TODO(), disgolink.WithNullTrack())
	}

	s.ChannelVoiceJoinManual(guildID, "", false, false)
}

func checkVoiceChannelEmpty(s *discordgo.Session, guildID string) {
	guild, err := s.State.Guild(guildID)
	if err != nil {
		return
	}

	botVCID := ""
	for _, vs := range guild.VoiceStates {
		if vs.UserID == s.State.User.ID {
			botVCID = vs.ChannelID
			break
		}
	}

	if botVCID == "" {
		return
	}

	humanCount := 0
	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == botVCID {
			member, err := s.State.Member(guildID, vs.UserID)
			if err == nil && member.User.Bot {
				continue
			}
			humanCount++
		}
	}

	if humanCount == 0 {
		stopAndDisconnect(s, guildID)
	}
}

func onTrackEnd(event *disgolink.PlayerTrackEndEvent) {
	if event.Reason == lavalink.TrackEndReasonLoadFailed {
		return
	}

	go func() {
		time.Sleep(1 * time.Second)

		guildID := event.Player.GuildID.String()
		gQueue := getGuildQueue(guildID)

		gQueue.Mu.Lock()
		defer gQueue.Mu.Unlock()

		currentTrack := event.Track

		switch gQueue.Loop {
		case LoopModeSingle:
			event.Player.Update(context.TODO(), disgolink.WithTrack(currentTrack), disgolink.WithPosition(0))

		case LoopModeQueue:
			gQueue.Tracks = append(gQueue.Tracks, currentTrack)
			if len(gQueue.Tracks) > 0 {
				nextTrack := gQueue.Tracks[0]
				gQueue.Tracks = gQueue.Tracks[1:]
				event.Player.Update(context.TODO(), disgolink.WithTrack(nextTrack), disgolink.WithPosition(0))
			}

		default:
			if len(gQueue.Tracks) > 0 {
				nextTrack := gQueue.Tracks[0]
				gQueue.Tracks = gQueue.Tracks[1:]
				event.Player.Update(context.TODO(), disgolink.WithTrack(nextTrack), disgolink.WithPosition(0))
			}
		}
	}()
}

func waitForExitSignal() {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}
