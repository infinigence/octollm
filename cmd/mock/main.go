package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/infinigence/octollm/pkg/engines/mock"
	ruleengine "github.com/infinigence/octollm/pkg/engines/rule-engine"
	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/octollm"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	exprenv.RegisterDefaultExtractor("promptTextLen", &ruleengine.PromptTextLenExtractor{})
	exprenv.RegisterDefaultExtractor("prefix20", &ruleengine.PrefixHashExtractor{Length: 20})
	exprenv.RegisterDefaultExtractor("suffix20", &ruleengine.SuffixHashExtractor{Length: 20})
	exprenv.RegisterDefaultExtractor("message5Hash", &ruleengine.Message5HashExtractor{})
	exprenv.RegisterDefaultExtractor("message5HashArray", &ruleengine.Message5HashArrayExtractor{})

	engine := mock.NewWithFixedOutput(`
归档

林夏的手指悬停在确认键上方已经三分钟了。全息屏幕上，那段记忆文件泛着淡蓝色的光，标签上写着：2019-2024，陈默。
"林先生，您还有四分钟的考虑时间。"AI管家的声音温和得不带一丝波澜，"根据《记忆伦理法》，情感记忆删除需要五分钟的强制冷静期。"
"我知道。"林夏收回手，端起已经凉透的咖啡。
这是2045年的春天，窗外是永不熄灭的霓虹。记忆存储技术已经普及了十年，人们可以像整理衣柜一样整理自己的大脑。删除一段记忆只需要三千信用点，比一顿像样的晚餐还便宜。
林夏第一次遇见陈默，是在2019年的秋天。那时候记忆存储还是新鲜事物，只有富人和病患才用得起。他们在一家旧书店相遇，陈默正在找一本绝版的《挪威的森林》，而林夏刚好从书架最高层抽出了那本泛黄的书。
"你也喜欢村上春树？"陈默的眼睛在那一刻亮了起来。
接下来的五年像是一部被快进的老电影。林夏记得陈默煮咖啡时总是先温杯，记得他左耳后有颗小痣，记得他在下雨天会哼走调的《Raindrops Keep Fallin' on My Head》。他们一起搬了三次家，养了一只叫"年糕"的橘猫，在2043年的冬天去了冰岛看极光。
然后2044年的春天，陈默在一场流感并发症中去世了。不是那种戏剧性的告别，只是某个平凡的周二早晨，他再也没有醒来。
起初林夏以为他能扛过去。他继续上班，继续喂猫，继续在周末去那家旧书店。但记忆是个狡猾的东西，它会在最意想不到的时刻袭击你——电梯里某个相似的背影，超市货架上他最爱的那款麦片，深夜半梦半醒时床另一侧的凹陷。
所以林夏来到了这里，来到了"记忆重置中心"。
"还有一分钟。"AI提醒道。
林夏闭上眼睛。他想起最后一次和陈默一起去海边，那是2043年的夏天。陈默的脚受了伤，不能下水，就坐在沙滩上给他画速写。夕阳把陈默的轮廓镀上一层金边，他在画纸背面写了一行字，然后把画塞进了林夏的口袋。
那行字是什么来着？
林夏突然站起身，动作大得碰翻了咖啡杯。
"取消操作。"他说。
"确认取消吗？三千信用点不会退还。"
"确认。"
林夏抓起外套冲出房间。他回到家，在床头柜最底层的抽屉里翻找——那里有一个铁盒，装着陈默留下的所有实体物品。明信片，电影票根，一枚生锈的钥匙扣。
那张速写就在最下面。纸张已经泛黄，背面的字迹有些模糊，但还能辨认：
"记忆会褪色，但爱不会。别把我存档，林夏。让我活在你的明天里。"
林夏坐在地板上，握着那张纸，终于哭了出来。
窗外，2045年的春天正盛大展开。年糕跳上他的膝盖，发出呼噜呼噜的声音。林夏擦掉眼泪，把速写贴在冰箱上。
有些重量，我们注定要背负一生。那不是负担，而是我们之所以成为我们的证明。
他没有删除那段记忆。相反，他决定再活久一点，久到足以创造更多值得记住的故事。

[完]
`, 100*time.Millisecond, 10*time.Millisecond)

	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", octollm.ChatCompletionsHandler(engine))
	mux.Handle("/v1/completions", octollm.CompletionsHandler(engine))
	mux.Handle("/v1/messages", octollm.MessagesHandler(engine))

	slog.Info("listening :8090")
	err := http.ListenAndServe(":8090", mux)
	slog.Error(fmt.Sprintf("server exited with error: %v", err))
}
