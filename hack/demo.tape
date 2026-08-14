Output demo.gif
Set Width 1100
Set Height 620
Set FontSize 15
Set Padding 20
Set Margin 36
Set MarginFill "#6B50C7"
Set BorderRadius 10
Set WindowBar Colorful
Set TypingSpeed 35ms
Set Theme "Catppuccin Mocha"

Hide
Type "tmux -f /dev/null new-session -s gifdemo"
Enter
Sleep 1s
Type "tmux set -g status off; tmux set -g pane-border-style fg=colour60"
Enter
Sleep 300ms
Type `export PROMPT="%F{magenta}❯%f " && cd /private/tmp/claude-501/-Users-harrison-Documents-code-Olsyn/496cf998-69c0-4900-91af-eeb6d8812436/scratchpad/gif/coolapp && export PATH=/tmp:$PATH && rm -f gerrymander.yaml && clear`
Enter
Sleep 500ms
Show

Type "gerry init"
Sleep 400ms
Enter
Sleep 2s

Type "cat gerrymander.yaml"
Sleep 300ms
Enter
Sleep 2.5s

Hide
Type `tmux split-window -h && tmux send-keys -t 1 'export PROMPT="%F{magenta}❯%f " && clear' Enter && tmux select-pane -t 0 && clear`
Enter
Sleep 800ms
Show

Type "gerry dev"
Sleep 400ms
Enter
Sleep 3s

Hide
Ctrl+B
Type "o"
Sleep 500ms
Show

Type "curl https://coolapp.test"
Sleep 400ms
Enter
Sleep 1.5s

Type "curl -sI https://coolapp.test | head -1"
Sleep 400ms
Enter
Sleep 4s
