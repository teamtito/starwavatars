class StarWarsAvatarsController < ApplicationController

    def show
      @starwavatar_avatar = StarwavatarAvatar.new(params[:id])
      respond_to do |wants|
        wants.svg  { render inline: @starwavatar_avatar.svg }
        wants.png  do
          send_data @starwavatar_avatar.png, filename: "#{params[:id]}.png",
            disposition: 'inline',
            quality: 90,
            type: "image/png"
        end
      end
    end

end
