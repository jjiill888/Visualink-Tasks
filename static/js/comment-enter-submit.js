// 评论区 Enter 发送，Shift+Enter 换行
(function(){
  var form = document.querySelector('.comment-form');
  if (!form) return;
  var textarea = form.querySelector('.comment-input');
  if (!textarea) return;
  // 设置提示
  textarea.placeholder = '添加评论，Enter 发送 · Shift+Enter 换行';
  // 监听键盘事件
  textarea.addEventListener('keydown', function(e){
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      form.querySelector('button[type="submit"]').click();
    }
  });
})();
