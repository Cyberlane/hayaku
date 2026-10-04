const workflow = document.querySelector('.workflow');
const replay = workflow.querySelector('.replay');

workflow.classList.add('animate');
replay.hidden = false;
replay.addEventListener('click', () => {
  workflow.getAnimations({ subtree: true }).forEach(animation => {
    animation.currentTime = 0;
    animation.play();
  });
});
